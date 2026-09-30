# Protocol reference

Every xbind endpoint, header, and wire format. This is the contract that
`bx`, the core web elements, and the SDKs are built on — anything here is
fair game for your own tooling.

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
Server-side, any request carrying
the cookie with the opaque-origin fingerprint — `Sec-Fetch-Site: cross-site`
(or `same-site`) on a non-navigation, or a non-GET navigation to `/api/*` or
`/ws/*` (form-POST CSRF) — has the **cookie dropped** before principal
resolution: a tile that omits its frame token cannot ride the human's
session. Requests with `Origin: null` (opaque-origin fetches) get
`Access-Control-Allow-Origin: null` and preflight answers — required for
tile `fetch()` to function at all, and safe because tile requests carry no
ambient credentials.

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
primary's responses are unchanged (§Tile deployments).

A partitioned tile's own call **addressing its global instance**
(`?xbin-partition=global`, §HTTP routes › Core) arrives there as the
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
class refuses it too, reads included (§Tile deployments, *Which deployment
a call acts on*).

**A partitioned tile's credentials act in a partition**
([partitions.md](/docs/partitions.md), in development). On a tile whose
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
  workspace policy `partitionConsent` on, consented: `403 <id> hasn't let
  <caller> use their <tile> data`); anything else reaches the callee's
  global instance, or `403 <tile> is partitioned: only partitioned tiles
  reach its people's data, and it has no global instance`;
- a non-primary deployment of a partitioned tile has one instance,
  `global`;
- the one way out of a partition by the URL: the tile's own frames,
  terminals, agent sessions and user-partition backends — and an admin
  calling it directly — reach its **global instance** with
  `?xbin-partition=global` (§HTTP routes › Core), attributed to the person.
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

```
GET  /healthz                    200 "ok", unauthenticated (liveness)
GET  /login                      login page; ?token=<root> sets the admin cookie
POST /login                      {username,password} form → session cookie (throttled)
GET  /login?invite=<tok>         invite set-password page (D22; single-use link)
GET  /login?impersonate=<tok>    redeems a view-as ticket (POST /api/xbin/
                                 impersonate): the signed-in minting admin's
                                 cookie becomes a read-only session as the
                                 user → 302 / (D64)
GET  /login?ticket=<t>&next=<path>
                                 a signed-in web view (the app's
                                 chrome tiles): spends a one-shot
                                 ticket the app's device session minted
                                 (POST /api/xbin/web-ticket; single use,
                                 60 s). A GET never signs a browser in: one
                                 already signed in as the same user → 302
                                 <path> (the ticket's own next); a signed-
                                 out one gets a "Continue as <name>" page
                                 naming the account (CSP frame-ancestors
                                 'none'), whose button posts to POST
                                 /login/web-ticket — login CSRF: a link to
                                 someone else's ticket, opened from a chat
                                 or a QR code, can't sign you in unseen.
                                 403 when a page started the navigation
                                 (Sec-Fetch-Site other than none), when
                                 the browser is signed in as someone else,
                                 when next was altered, or once the device
                                 session, the device or the account is
                                 gone (or an SSO-bound account's window
                                 closed, D93). A HEAD answers 405 and
                                 leaves it unspent. Throttled
POST /login/web-ticket           {confirm} form, from that page only:
                                 Sec-Fetch-Site same-origin (or, without
                                 Fetch Metadata, an Origin of this host;
                                 neither → 403), the confirm nonce equal to
                                 the cookie the page's response set (this
                                 browser), single use, 2 min → the cookie
                                 session, 303 <path>. The session ends
                                 with the device and the app's sign-out
                                 and keeps the device login's time
                                 (docs/auth.md §Device login). Throttled;
                                 audit-logged
POST /login/invite               {invite,password,password2} form → redeems the
                                 invite (sets the password, consumes the link),
                                 signs the user in (throttled)
GET  /login/sso                  SSO sign-in start (docs/auth.md §SSO; 404 when
                                 not configured): redirects to the IdP with
                                 PKCE + state + nonce, carried in a signed
                                 short-TTL cookie (throttled)
GET  /login/sso/callback         the IdP's return leg: verifies state and the
                                 ID token (or fetches GitHub's verified
                                 primary email), resolves the email to a user
                                 (bound Email first, else the domain
                                 allow-rule JIT-provisions), then mints the
                                 same session cookie as password login.
                                 Errors land back on /login as fixed
                                 ?sso_err= codes (throttled; audit-logged);
                                 held: a change of the provider its person
                                 hasn't confirmed (docs/partitions.md
                                 §Credential resets)
GET  /login/sso?app=1&challenge=<c>
                                 the native app's SSO sign-in (in
                                 ASWebAuthenticationSession): c =
                                 base64url(sha256(verifier)), a PKCE S256
                                 challenge the app chose. Same IdP round
                                 trip, but the callback ends in 302
                                 xbin://sso?ticket=<one-shot> (2 min) —
                                 no cookie — or xbin://sso?error=<code>
POST /login/ticket               {ticket, verifier} → the app token
                                 response (below): redeems that ticket
                                 when base64url(sha256(verifier)) matches
                                 the challenge — another app that caught
                                 the redirect can't. Single use (throttled)
POST /login/device/challenge     {deviceId} → {nonce, expires}: a single-
                                 use login nonce for one enrolled device
                                 (60 s; docs/auth.md §Device login). 404:
                                 no such device (revoked — enroll again).
                                 Throttled
POST /login/device               {deviceId, nonce, signature} → {token,
                                 tokenType:"Bearer", user:{id,name,role},
                                 deviceId, expiresIdle, expiresMax}
                                 (unix seconds): signature = the device
                                 key's ECDSA P-256 / SHA-256 signature
                                 (ASN.1 DER, base64url) over
                                 "xbin-device-login-v1\n" + origin + "\n" +
                                 deviceId + "\n" + nonce, origin being the
                                 one the device enrolled with
                                 (native/spec/device-login.md). Opens a
                                 human session used as Authorization:
                                 Bearer — the same principal and TTLs as a
                                 browser session (Via "device"). 401: nonce
                                 spent/expired/not this device's, or a bad
                                 signature; 403: account disabled — or, for
                                 an account whose only way in is SSO (SSO
                                 configured, and no password, or a non-admin
                                 in SSO-only mode), {error, reauth:"sso"}
                                 when their last SSO sign-in is older than
                                 the session max TTL (sign in with SSO
                                 again; the device stays). There, expiresMax
                                 is also capped at that last SSO sign-in +
                                 the max TTL.
                                 Throttled; audit-logged
POST /logout                     revoke the session (cookie → 302 /login;
                                 an app session's Authorization: Bearer →
                                 204; a device-key session signs the device
                                 out: every session opened with its key
                                 ends — earlier ones the app replaced, and
                                 the web sessions any of them opened —
                                 and the device loses its push
                                 registration; it stays enrolled)
GET  /                           redirect /c/root/
GET  /c/<component-path>/[file]  component static files; HTML gets the
                                 <head> injection (import map, component
                                 meta, frame token, xbin-client.js) unless
                                 manifest inject:false. Cache-Control: no-store.
                                 Auth: any principal that may read the tile
                                 (cookie RBAC; frame token for the tile
                                 itself; an element holding a code[:<tile>]
                                 grant reads its source here too). The
                                 injected frame token is minted only for a
                                 human or the tile itself (its own frame/
                                 terminal token, or an xbin.window sub-path
                                 of it) — never a backend's instance token,
                                 which names no person; any OTHER tile's
                                 element principal — code grant or its
                                 user's access — gets the HTML with
                                 content="";
                                 a tile's SUBRESOURCE loads
                                 (Sec-Fetch-Dest: script/style/image/font/
                                 media/worker, never documents or fetch;
                                 never .html) are also authorized
                                 credential-less by the opaque-origin
                                 fingerprint (Sec-Fetch-Site: cross-site/
                                 same-site — sandboxed frames strip cookies
                                 AND the Referer, so that is the only signal)
                                 PLUS a recently-authenticated source IP
                                 (a successful auth from that IP within the
                                 last hour — the fingerprint alone is
                                 spoofable by non-browser clients, so drive-
                                 by scanners with no login get 401; tile
                                 source is still not where secrets live).
                                 Non-chrome HTML responses carry CSP sandbox;
                                 all responses X-Content-Type-Options: nosniff.
                                 A request from the xbin app (X-XBin-Client:
                                 app/<version>) also gets <meta name=
                                 "xbin-ws-origin" content="wss://<host>">
                                 (ws:// without TLS or a proxy's
                                 X-Forwarded-Proto: https; the Host the
                                 client used) in injected HTML: xbin-client
                                 opens xbin.ws and /ws/events there.
                                 A sandboxed tile's non-document files
                                 (anything but .html/.htm, any case) carry
                                 `Content-Security-Policy: sandbox` in every
                                 mode (PDF excepted); a symlink resolving
                                 outside the workspace or into .xbin/, data/
                                 or homes/, a FIFO or a device → 404.
                                 The frame token is injected only for a human
                                 or the tile itself (or a navigation within
                                 the tile's own tree of nested components,
                                 when everyone who can write the page
                                 navigating can write the target).
                                 That credential-less rule is the LEGACY mode
                                 (--tile-assets=legacy, the default this
                                 release; removed in the next). Under the
                                 strict modes (tokens, origins) there is no
                                 credential-less path: every /c/ request needs
                                 one of the credentials above (401 otherwise,
                                 body naming the fix), a tile's own frame
                                 token is re-checked against its USER's live
                                 access, and files are opened beneath their
                                 tile, reached without any symlink (a symlink
                                 leaving the tile → 404). docs/auth.md §Tile
                                 asset gating.
                                 The bare URL serves the tile's primary
                                 (docs/tile-deployments.md): while the
                                 primary is pinned it serves the pinned
                                 checkpoint, in every mode, opened
                                 beneath it; saves don't show. A symlink in
                                 it that leaves the checkpoint → 404; a
                                 deps/<name>/… path is served as the /c/
                                 path of the tile the checkpoint's
                                 deps/<name> link names, under that tile's
                                 own access rules.
                                 /c/<tile>+<name>/[file] serves deployment
                                 <name> of a tile that has deployments —
                                 people with write on the tile, and the
                                 tile's own credentials bound to <name>;
                                 resolution, the gate and what its
                                 documents carry: §Tile deployments,
                                 *Deployment URLs*
GET  /c/<component-path>/?native=1
                                 the tile's native runtime document
                                 (docs/elements.md §Native app UI), generated
                                 by xbind: the injection above (also under
                                 inject:false) + <meta name="xbin-native"
                                 content="1"> + a module script importing
                                 /vendor/xb-native.js, then loading
                                 ./<native entry> with its boot() (an entry
                                 that fails to load reports {op:"error",
                                 kind:"module"} to the app).
                                 &preview=1 adds <meta name=
                                 "xbin-native-preview" content="1"> and
                                 imports /vendor/xb/preview-host.js before
                                 the entry (a missing one is logged, not
                                 fatal). Auth, CSP sandbox, headers and the
                                 frame-token rule as the tile's index.html
                                 (another tile fetching it gets no token);
                                 404 with the reason
                                 when the tile has no native entry (trusted
                                 chrome never has one); 410 with the reason
                                 while an admin has turned native tile UIs
                                 off for the workspace (PUT /api/xbin/
                                 native-runtime) — &preview=1 is still
                                 served; the slashless URL
                                 301s keeping the query; any other
                                 directory URL 404s. ?native=1 on a file
                                 URL is an ordinary request. It is
                                 generated from the primary's code (the
                                 pinned checkpoint's native entry while the
                                 primary is pinned), so it always agrees
                                 with /components' native;
                                 /c/<tile>+<name>/?native=1 from deployment
                                 <name>'s own code, for the principals that
                                 may open its documents
GET  /c/~<asset-token>/<component-path>/<file>
                                 tokens mode only: the asset-token plane the
                                 injected <base> points at (docs/elements.md
                                 §Asset URLs). A non-document static file of a
                                 tile the token's user may read — checked live
                                 for the token's tile AND the tile loaded, so
                                 cross-tile loads work exactly when the user
                                 can read the other tile. Never HTML, a
                                 directory, chrome (root/shell) or a document
                                 destination (Sec-Fetch-Dest document/iframe/
                                 embed/object, or a navigation) → 403; GET/HEAD
                                 only; invalid/expired/revoked token → 401.
                                 Answers carry CSP sandbox + Referrer-Policy:
                                 no-referrer. The token authenticates nothing
                                 else (not /api, not /c/ without the prefix,
                                 not a frame token). A deployment's files are
                                 /c/~<asset-token>/<tile>+<name>/<file>: the
                                 token is unchanged, and for a deployment
                                 other than the primary its user must write
                                 the tile, checked live.
(tile origin)                    origins mode only: the host
                                 t-<id>.<tiles-domain> is tile <id>'s own
                                 origin (id = keyed hash of the tile path; the
                                 URL is /components' `origin`). It serves
                                 ONLY: GET /c/… (?xbin_begin=<hint> on a
                                 navigation starts the exchange: a tile cookie
                                 already bound to the login the hint names →
                                 302 to the same URL without it; else the
                                 exchange state cookie __Host-xbin_tstate
                                 (xbin_tstate on plain http; HttpOnly,
                                 SameSite=Lax, host-only, 2 min, kept while
                                 present) and a 302 to the same path on
                                 --external-url with ?xbin_state=<state>.
                                 ?xbin_ticket=<ticket> on a navigation — the
                                 one-time ticket the workspace's second leg
                                 carries, bound to the browser session AND
                                 that state — is redeemed for the cookie
                                 __Host-xbin_tile (xbin_tile on plain http):
                                 HttpOnly, Secure, SameSite=Strict,
                                 host-only, Path=/, living as long as the
                                 browser session it is bound to — and 302'd to
                                 the same URL without it; the ticket's state
                                 must be this browser's (a ticket from
                                 another session can't sign it in: login
                                 CSRF), its tile this origin's, its session
                                 live, its user able to read the tile; a
                                 cross-site initiator is not exchanged; a
                                 failed exchange never redirects), /api/… and
                                 /ws/events (as the tile's frame principal;
                                 read-only in a view-as session; the tile
                                 cookie is stripped before a backend sees the
                                 request, and Set-Cookie is dropped from every
                                 /api answer), /vendor/, /healthz. A document
                                 is served only on the cookie (a navigation
                                 with ?frame= and a valid cookie is 302'd
                                 without it). Every /c/ answer carries CSP
                                 frame-ancestors 'self' <--external-url>.
                                 A browser navigating to anything else —
                                 a workspace page (/login, /docs/, /), chrome,
                                 another tile's page — is 302'd to the same
                                 path on --external-url (links built from
                                 location.origin keep working); a top-level
                                 navigation to the tile's own page without a
                                 valid cookie goes once through the workspace
                                 for a fresh ticket (marked ?xbin_retry=1 so
                                 it can't loop; not for cross-site
                                 initiators); a framed one gets a page asking
                                 for a reload. /c/ is authorized live for the
                                 cookie's user on the tile loaded; another
                                 tile's files are served only as non-documents
                                 with CSP sandbox; chrome is not served. The
                                 cookie alone is honoured only from the origin
                                 itself (Sec-Fetch-Site same-origin/none; a
                                 same-site navigation to /c/ — the shell
                                 framing the tile; never a foreign Origin on a
                                 write or WebSocket); two tile cookies = none.
                                 An explicit frame token must be this tile's.
                                 Each deployment of a tile has an origin of
                                 its own: main's is the tile's t-<id>, any
                                 other deployment's another t-<id> keyed on
                                 the tile and the deployment's name (so its
                                 localStorage and IndexedDB are its own,
                                 whichever deployment is primary). On a
                                 deployment's origin both /c/<tile>/… and
                                 /c/<tile>+<name>/… serve that deployment,
                                 /api/<tile>/… acts as its frame principal,
                                 and a path naming another deployment of
                                 the tile is 302'd to the workspace; a
                                 non-primary deployment's origin needs write
                                 on the tile, checked on every request. Its
                                 ticket and cookie are the x2 and c2 forms
                                 (seven dot-separated parts, the deployment
                                 among them, MAC-covered; opaque); main's
                                 keep x1 and c1, and an xbind older than
                                 tile deployments refuses x2 and c2.
(workspace, origins mode)        A browser navigating to a sandboxed tile's
                                 document on --external-url (a human, or the
                                 tile itself, with a live session) is sent to
                                 its tile origin with ?xbin_begin=<hint of the
                                 session's binding>: a 302
                                 when the initiator is the workspace or the
                                 user (Sec-Fetch-Site same-origin/none, or no
                                 Fetch Metadata and no tile-origin Referer, or
                                 the tile's own tree moving its frame); else
                                 (cross-site, same-site) a 200 page with a
                                 meta refresh there (frame-ancestors 'none',
                                 X-Frame-Options DENY). Otherwise 403: no tile
                                 document is served to a browser on the
                                 workspace origin. The exchange's second leg —
                                 the same navigation back from the tile
                                 origin with ?xbin_state=<state> (not
                                 cross-site) — is a 302 to the tile origin
                                 with ?xbin_ticket=, bound to the session and
                                 that state. Header-credentialed clients
                                 (X-XBin-Frame-Token, Authorization) get the
                                 document. The bare /c/<tile>/ goes to the
                                 primary's origin (the one /components
                                 reports), /c/<tile>+<name>/ to that
                                 deployment's (GET /api/xbin/deployments
                                 names each deployment's origin). Every
                                 workspace request a tile origin starts
                                 has its cookies dropped:
                                 Sec-Fetch-Site same-site unless a top-level
                                 navigation or that second leg (a navigation
                                 to a sandboxed tile's /c/ document with
                                 ?xbin_state=), an Origin on the tiles
                                 domain, a Referer on it unless a
                                 navigation. Chrome
                                 documents carry frame-ancestors 'self'. The
                                 session cookie is __Host-xbin_session on
                                 https and *.localhost (xbin_session is then
                                 not read).
GET  /vendor/<file>              core elements + vendored libs (lit, xterm…);
                                 UNAUTHENTICATED — shipped xbind code, and
                                 sandboxed tile frames load it credential-less
GET  /docs/<file>.md             these docs (HTML viewer for browsers; ?raw=1
                                 or non-HTML Accept for plain markdown)
ANY  /api/<component-path>/<p>   → component backend (see below)
ANY  /api/xbin/<p>              → xbind's own API (below)
ANY  /api/~<ticket>/<p>         → a path ticket's prefix of one tile's API
                                 (§Path tickets below, D135)
```

`/api/<component>` resolution is longest-prefix over registered components;
the remainder is the backend path, decoded: an encoded `/` (`%2F`) is a
separator there, and `.` and `..` segments are resolved (xbind's router
redirects a plain dot segment first). So a backend routes — and checks — the
path a request names: a caller can't smuggle a `/` into one segment's value
(the `/api/xbin` routes below keep an encoded `/` in its segment instead).
A tile's bare `/api/<self>/` call reaches
the caller's own deployment (a self-call, at `admin`); anyone else's bare
call reaches the primary. `/api/<tile>+<name>/<p>` reaches deployment
`<name>` of a tile that has deployments: admins, and the tile's own
credentials bound to it; other tiles use the bare URL only, the primary's
name included; a person with write passes the URL gate and then holds no
role there, as on the bare URL. The qualifier is consumed: the backend sees
`/<p>` (§Tile deployments, *Deployment URLs*). Responses stream
(SSE/chunked flush immediately) and WebSocket upgrades pass through; a `?frame=` query
credential is accepted for browser WS attribution and is consumed by xbind
(stripped before forwarding). Backends with active streams are exempt from
idle reaping; streams still end at the callee's blue/green drain (D8).
**Path tickets** (D135). A tile's page mints one with `POST
/api/xbin/path-tickets {path}` (its frame token; below) for a prefix of
its own tile's API, and `/api/~<ticket>/<p>` then reaches
`/api/<tile>/<prefix>/<p>` — any method, WebSocket upgrades too — as that
page's frame principal (the tile, the person, the login's deployment and
view-as binding), and nothing else: never another route of the tile,
another tile, `/api/xbin` or `/ws`. It is for a document the page frames
from its own backend in an opaque-origin sandbox — the agent template's
live preview of a server in a sandbox — whose relative loads carry no
token and no cookie; the framed content can read the ticket in its own URL,
and all it reaches is the prefix it already is. A `<p>` segment that
decodes to `.` or `..` is 400; the ticket works only from an address
that signed in within the hour (the `/c/` subresource rule's; 401
otherwise), dies with the login that minted it, expires after 12 h (401),
and on a tile origin only that tile's tickets are served (403). A request
without the slash after the ticket is redirected to it. Cookies and
`Authorization` never pass through it, a backend's `Set-Cookie` never
comes back, and every answer carries `Content-Security-Policy: sandbox
allow-scripts allow-forms` and `nosniff` whatever the backend sets (an
opaque origin, even opened directly). The person must still read the tile
at every use.

Errors are JSON:
`{"error": "...", "docs": "/docs/...", "detail": "compiler output"?}` —
404 unknown component, 403 no grant, 410 a tile whose manifest declares the
removed runtime `cgi` (the error says so; its code never runs — D117), 502
build/backend failure (build failures carry compiler output in `detail`).
409 for a call that would reach the primary of a tile paused by its
partition mode — its code asks for another `partition` than the one
recorded, on a tile that holds data, or for an invalid one: no instance of
the primary runs until a tile manager decides (§Manifest "partition" in
[elements.md](/docs/elements.md)). The body adds `partition`:
`{"error": "<tile> is paused: a partition mode switch is requested (<R> →
<Q>); a manager of <tile> must switch (deleting all its data) or keep the
current mode", "docs": …, "partition": {"state": "pending", "from":
{user, global}, "to": {user, global}}}`, or for an invalid request
`{"error": "<tile> doesn't run: partition: <why>", "docs": …,
"partition": {"state": "invalid", "error": "partition: <why>"}}`. A caller
the tile refuses gets its 403 first; public ingress to such a tile answers
503 as for a disabled one. The clause after "must switch" says what the
switch deletes: `deleting all its data` between user partitions and
unpartitioned, otherwise `deleting nothing (…)` when `"global"` comes or
`deleting the global instance's data …` when it goes. While a switch is
pending, a document load of the tile is xbind's own page instead of the
tile's — 409, `Cache-Control: no-store`, a `sandbox` CSP with no scripts —
saying a switch is requested (R → Q), what it deletes (all data in the
tile will be deleted for it to happen, or, when `"global"` comes or goes,
nothing or the global instance's data), the tile's `partitionNote` (as
text), and who decides where (`POST /api/xbin/partitions/mode`, `bx
partition switch|keep`). A document load is a request with `Sec-Fetch-Dest`
`document`, `iframe`, `frame`, `embed` or `object`, or — without Fetch
Metadata — a GET of `/c/<tile>/…/`, an `.html` file or `?native=1` under a
login session or by the tile's own frame (the app). Any other read (a
fetch, a script, a bearer token, another tile's code grant) and the tile's
other files are served as before. The primary's deployment URL
(`/c/<tile>+<primary>/`, and a deployment origin's bare URL) shows the page
too; another deployment's URL isn't paused.
On a partitioned tile a call reaches the
partition §Authentication names (403 with the reason when it reaches none),
and a person's partition that can't start now answers 503 with why;
public ingress reaches only the tile's global instance, and a tile without
one answers 503 `this site is not being served right now`.
**`?xbin-partition=global`** on a call to a partitioned tile addresses the
tile's global instance instead of the caller's partition
([partitions.md](/docs/partitions.md) §The global instance and people's
partitions). xbind consumes it — the backend never sees it; the rest of
the query passes — on partitioned tiles only (their recorded mode has user
partitions, or can't be read), public ingress to one included (which
reaches global anyway); on every other tile it reaches the backend as any
query parameter, as it always has. It is honoured for the tile's own
frames, terminals, agent sessions and user-partition backends, whose call
reaches the global instance attributed to the partition's person
(§Authentication, after the identity headers), and for an admin calling
the tile directly, who keeps their own role (a reader or writer calling
`/api/<tile>/…` by themselves is refused with or without it, `403 user:<id>
is not granted access to <tile>`: their way in is the tile's frame). A
view-as frame reaches it as the viewed person with `X-XBin-Role: reader`,
whatever their level, and `X-XBin-Viewed-By`; its writes are refused (403
`read-only: …`) as everywhere. For credentials already in `global` — the global
instance, the owner token and its frames and terminals, a non-primary
deployment's credentials (whose one instance is `global`) — it changes
nothing. Refused: a value other than one `global` (400 `?xbin-partition
addresses a partitioned tile's global instance: its one value is global`);
another tile's credentials (`403 ?xbin-partition=global addresses a tile's
own global instance: <caller> can't use it on <tile>`); cron, bus and mail
deliveries (`403 a <cron|bus|mail> delivery acts in the partition it was
registered for: …` — and a partitioned tile's `PUT /cron/jobs` or `PUT
/bus/subscriptions` whose `path` carries the parameter answers 400 with
those words when it is registered); a path ticket (`403 a path ticket reaches its own
partition only: ?xbin-partition=global needs the page's own frame
token`); a tile without a global instance (`404 <tile> has no global
instance` — the one 404 with that text: the same words as a 403 are the
refusal of a credential whose own partition is `global`, such as the
global instance's token or a global cron job, on a tile without one); and
a person who can't read the tile, is disabled or deleted
(the partition refusals of §Authentication). The client's
`xbin.fetch(url, {partition: 'global'})` and the Go SDK's
`xbin.GlobalURL(path)` add it, from a user partition only.

### xbind API (`/api/xbin/…`)

A path is routed segment by segment as it was sent: an encoded `/` (`%2F`)
stays part of its segment's value and never reaches another route (a
catch-all route — `kv`, `blob`, `vault` — sees the decoded value, as it
always did), and a segment that decodes to `.` or `..` (`%2E`) is 400
`{"error": …, "refusal": "invalid"}`.

```
GET    /status                     admin. terminals ({id,cwd,net,kind,vm,user,
                                   …}), component count, host
                                   {cpuBusy,cpuTotal,memTotal,memAvail,
                                   diskTotal,diskFree}, traffic
                                   {reqs,bytesOut,uptimeSec} (cumulative —
                                   delta two polls for rates), and version (the
                                   running xbind build commit). A terminal
                                   whose session targets a named deployment
                                   carries deployment; a person's session
                                   on a partitioned tile carries partition
                                   ("user:<id>", or "global" when it
                                   targets a non-primary deployment) and
                                   an empty name
GET    /backends                   admin. per-component backend state
                                   {<path>: {state, gen, error?}}. The row
                                   stays the primary's; a tile with
                                   deployments adds deployment (the primary's
                                   name) and, for admins in a person's own
                                   session, deployments: {<name>: {state, gen,
                                   error?}} of its other deployments that
                                   run. A tile running only another
                                   deployment shows the primary's idle row
                                   {state: idle, gen: 0}. Other tiles'
                                   credentials an xbin grant makes admin get
                                   the primary rows only. A partitioned
                                   tile's row adds partitions: [{partition,
                                   state, gen, uptimeSec, rssKb, restarts,
                                   crashLoop?, lastExit?, lastStarted?,
                                   errorClass?}] — its people's running
                                   instances, metadata only (errorClass:
                                   crash-loop | build | start | exit |
                                   other, never the text); a tile whose
                                   global instance isn't running gets the
                                   idle row to hold them
GET    /runtime                    admin. full runtime visibility →
                                   {host:{version,kernel,pid,uid,numCPU,goroutines,
                                   heapMB,uptimeSec,isolate,rootfs,scopeUids,
                                   protections:{seccomp,landlock,landlockAbi}},
                                   backends:[{path,runtime,state,isolated,
                                   sandbox,vm?:{memMiB,vcpus,emulated?},
                                   checkpoint?,pid,gen,
                                   uptimeSec,restarts,activeConns,rssKb,threads,fds,
                                   cpuSec,namespaces:{<ns>:{id,isolated}},egress:[…],
                                   netRef?,net?,netSource?,netNote?,
                                   cgroup:{memCurrent,memMax,cpuUsec,pidsCurrent},
                                   activity:{allowed,denied,active,txBytes,rxBytes,
                                   recent:[{proto,dst,port,allowed,txBytes,rxBytes,
                                   start,end}]}}], resources:[{id,type,size,detail}],
                                   stats:{cgroup,intervalSec,tiles:[{path,owner,
                                   cur:{t,cpu,mem,rbps,wbps,riops,wiops,pids},
                                   series:[point…]}]}} — stats is the live
                                   per-tile sampler (~2s cadence, ~3 min of
                                   points; demand-driven — sampling runs only
                                   while /runtime is being polled). cpu is % of
                                   one core; rbps/wbps + riops/wiops are
                                   syscall-level I/O rates (includes FUSE-backed
                                   resource I/O); cgroup=true means exact
                                   whole-tree accounting via cgroup v2
                                   delegation, false = /proc-tree sampling
                                   {state: idle|building|healthy|failed, gen, error?}.
                                   netRef = the bound/defaulted net ref (`org`,
                                   `internet`, `lan:…`, a provider), net = the
                                   effective mode host|relay|splice|none,
                                   netSource = "org:<id> (<sets>)" for org
                                   reach, netNote = why a stored binding is
                                   inert (D54). sandbox = how the backend is
                                   isolated, vm | namespace | host (a running
                                   generation's, else how it would start;
                                   D112); vm = a VM generation's guest size
                                   and whether it is emulated. For a VM
                                   backend pid, namespaces, rssKb, threads and
                                   fds describe its host-side jail (the shim,
                                   whose child VMM holds guest memory) — read
                                   cgroup for what the VM uses. checkpoint:
                                   the full tree id of the checkpoint the
                                   running generation runs (live reload
                                   paused); absent while it runs the work
                                   tree. backends[] keeps one row per tile,
                                   the primary's, which gains deployment on a
                                   tile with deployments; the others' rows (the
                                   same shape plus deployment) are listed in a
                                   top-level deploymentBackends[], to people
                                   only, absent while none runs. resources[]
                                   rows gain deployment on a scope with a
                                   deployment record or deployments beyond
                                   main ("main" for main's), and a scope's
                                   data beyond main has rows of its own (kv,
                                   blob, sqlite, filesystem), one per
                                   deployment's data. A partitioned scope's
                                   people's partitions have rows of their own
                                   too, one per partition and resource that
                                   isn't shared, with deployment and
                                   partition ("user:<id>"; the partition id
                                   when its record can't be read)
GET    /gpus                       admin. host NVIDIA GPUs for gpu:* grants and
                                   the terminal picker → {gpus:[{index,uuid,
                                   name,node}]}
GET    /vm                         authenticated. VM sandboxes (D89)
                                   → {status:{available,reason,emulated?,
                                   note?}, policy:
                                   {terminals,backends,memMiB,vcpus,maxVMs,
                                   budgetMiB,diskGiB,tiles,tilesBudgetMiB,
                                   tilesEmulated}, used?:{vms,memMiB},
                                   usedTiles?:{vms,memMiB}} (used,
                                   usedTiles: admins; usedTiles is the part
                                   of used that tile sandboxes hold).
                                   available=false names why: no /dev/kvm, the
                                   xbind user not in the kvm group, a missing
                                   asset (firecracker, vmlinux, xbin-vmagent,
                                   mkfs.erofs, a static bx), no --isolate.
                                   emulated=true: no usable KVM, so VMs run
                                   under QEMU's emulation, much slower (D90);
                                   note says why
PUT    /vm/policy                  admin. body: any of {terminals,backends,
                                   memMiB,vcpus,maxVMs,budgetMiB,diskGiB,
                                   tiles,tilesBudgetMiB,tilesEmulated},
                                   merged onto the stored policy (a field the
                                   body leaves out keeps its value) →
                                   {status, policy, stored}.
                                   Off by default (the installer writes
                                   terminals on, backends and tiles on with
                                   KVM, for a workspace with no policy: D110,
                                   D120); zero
                                   sizes = defaults (2048 MiB, 2 vCPUs, 8
                                   VMs, budget maxVMs×memMiB,
                                   a 20 GiB VM terminal disk — grown, never
                                   shrunk). Turning backends off stops new VM
                                   generations; running ones keep going.
                                   tiles: the sandboxes manager tiles run
                                   (cap:sandboxes) may use VM mode; their VMs
                                   also count against tilesBudgetMiB (0 =
                                   half the budget; at most budgetMiB);
                                   tilesEmulated also allows it where VMs run
                                   emulated. Turning tiles off stops the
                                   running tile VM sandboxes, and
                                   tilesEmulated off those running emulated
                                   (state kept; a start answers 503 with the
                                   reason until it is back on). 400 on
                                   unknown fields or out-of-range sizes, 409
                                   without --isolate
GET    /sandboxes?tile=            admin. every sandbox xbind runs (D112) →
                                   {sandboxes:[{id,kind (backend|terminal|
                                   agent|tile),tile,parent?,user?,label?,
                                   for?,forUser?,partition? (a person's
                                   partition's backend: user:<id>),mode
                                   (vm|namespace|host),accel? (kvm|emulate),
                                   memMiB?,vcpus?,pid,gen?,started,leaf?,
                                   disk?,net?,restricted?,owner?,name?,
                                   status?,uptimeSec,stats?:{cpu,mem,pids,
                                   scope}}], disks:[{kind (terminal|tile|
                                   person-terminal),
                                   key,sandbox?,sandboxUid?,path,tile?,
                                   apparentBytes,allocatedBytes,inUse}],
                                   failures:[{time,kind,tile,partition?,user?,mode,stage
                                   (refused|start|health|exit),error,count}],
                                   failureCounts:{<stage>:n}, cgroup,
                                   intervalSec, health:{isolation:{tier,
                                   isolate,rootfs,scopeUids,protections,
                                   cgroup,uidRange?,uidRangeNote?},vm:{
                                   available,reason?,emulated?,note?,accel?,
                                   forced?,assets:{piece:path},missing?:[…],
                                   kvm?,emulation?,policy,stored,used,
                                   usedTiles,usedBy:{<tile>:{vms,memMiB}}},
                                   tileSandboxes:{cgroup,flows:{used,cap},
                                   total:{memMiB:{used,cap},pids:{used,cap}},
                                   policyError,lowDisk,trash:{entries,
                                   bytes}}}}.
                                   A backend
                                   is listed per generation (blue/green shows
                                   two; stats scope "tile" is the tile's
                                   shared leaf — count it once), a session
                                   by its id; stats are the live sampler's
                                   (demand-driven, like /runtime). stored is
                                   the VM policy as set (0 = default) — what
                                   an editor PUTs back. disks: the VM disks
                                   on the host — a tile's terminal layer's
                                   (kind terminal), its tile sandboxes'
                                   (kind tile, with the sandbox's name and
                                   uid) and, on a partitioned tile, each
                                   person's terminal layer's (kind
                                   person-terminal: key is the tile's
                                   storage key, the person unnamed). A
                                   person's terminal or agent session on a
                                   partitioned tile is listed without its
                                   name. A running tile sandbox (D120) is a
                                   kind tile row: its name, its manager's
                                   claims for/forUser, stats from its own
                                   leaf. health.tileSandboxes: why tile
                                   sandboxes run without cgroup limits
                                   although xbind's cgroup is delegated
                                   (cgroup; "" = they have them, or nothing
                                   does), their relays' shared cap on
                                   concurrent connections (flows), the
                                   memory the running ones may take of the
                                   policy's total (total.memMiB, MiB; cap 0 =
                                   none) and the processes they hold of it
                                   (total.pids; used -1 = unknown, no
                                   cgroup), why the sandboxes policy file
                                   can't be read (policyError; "" = it can —
                                   tile sandboxes are off while it can't),
                                   whether starts are held for a low disk
                                   (lowDisk), and the state deleted or reset
                                   that waits for its confined removal
                                   (trash: entries, bytes as last measured).
                                   failures: the newest 64, identical ones
                                   within 10 min coalesced (count). ?tile=
                                   narrows.
                                   Deployments follow the registry's name
                                   rule: main's backend rows are unchanged
                                   (id backend:<key>:g<gen>, no deployment,
                                   stats scope "tile"); another deployment's
                                   have the id backend+<name>:<key>:g<gen>,
                                   deployment, tile (the tile's path) and
                                   stats scope "deployment" (its cgroup leaf,
                                   shared by its generations). While a tile
                                   runs a deployment beyond main, main's
                                   "tile" stats are its own leaf's, so a
                                   tile's rows sum per leaf. leaf is the
                                   generation's own: the flat <key> while main
                                   runs alone, tile-<key>/d-<name>/backend
                                   otherwise. A person's partition's backend
                                   (docs/partitions.md) has the id
                                   backend@<pkey>:<key>:g<gen>, partition
                                   (user:<id>), its own leaf
                                   tile-<key>/p-<hash>/backend and stats
                                   scope "partition"; metadata only.
                                   failures[] of another deployment
                                   carry deployment? (a start past the caps
                                   or the VM room of §Tile deployments shows
                                   here, stage refused).
                                   &deployment=<name> narrows rows and
                                   failures (main: the rows without a
                                   deployment) and is echoed as a top-level
                                   deployment; a malformed name → 400.
                                   health.vm.usedBy stays keyed by tile.
                                   tileSandboxes:[{tile,name,uid,state,
                                   stateDetail?,mode,accel?,memMiB,vcpus,
                                   diskGiB,diskBytes,for?,forUser?,
                                   lastActive?,tileExists,agentPorts?,
                                   ports?}] — every tile
                                   sandbox definition (D120), stopped ones
                                   and those of removed tiles too (an admin
                                   stops or deletes one with ?tile=).
                                   agentPorts: whether the running one's
                                   in-box agent serves ports (serves |
                                   predates — restart it), as a port
                                   request found; ports: its latest 8 port
                                   requests (D135), newest last, [{at,port,
                                   status?,refusal?,from}] (from: the calling
                                   manager tile); both absent until one. A manager
                                   tile's backend gets its own tile sandboxes
                                   instead (§Tile sandboxes)
GET    /tile-status?component=<p>  self or admin. one tile's runtime metrics —
                                   backend {state,gen,sandbox,vm?,cpuSec,cgroup:{mem,pids},
                                   rssKb,fds,activeConns,egress}, disk {usage,
                                   quota,blocked}, alerts[], net {netRef, net,
                                   netRules, netSource, netNote} (the effective
                                   network, D54). Readable from that tile's
                                   terminal (tile-scoped token). `bx status`
                                   renders it.
                                   &deployment=<name>: the deployment reported
                                   (default: the caller's bound deployment —
                                   a tile credential's — else the primary).
                                   400 a malformed name, and a component=
                                   that names a deployment as tile+name
                                   (D127j: "a deployment is named with
                                   deployment=, …"); 403 "a tile's own
                                   credentials act only on their own
                                   deployment (<bound>)" (a tile's frames and
                                   backends read their own only); 403
                                   "deployments of <tile> need write access"
                                   for a non-primary deployment and a caller
                                   outside its audience, whether or not it
                                   exists (a frame bound beyond the primary
                                   whose user no longer writes the tile
                                   included); 403 for a session that follows
                                   a protected primary; 404 unknown. The
                                   answer gains deployment on a tile with
                                   deployments or when one was named (the
                                   echo), and for admins and the tile's
                                   terminal/agent tokens whose user writes it
                                   deployments: {primary, liveReload,
                                   items:[{name, state, gen, checkpoint?}]}
                                   (checkpoint: the pinned id). A person's
                                   partition's own credential (its backend,
                                   the person's frames, terminals, agent
                                   sessions) gets its partition's: backend
                                   (its instance, or null), disk (the
                                   partition's bytes and ceiling) and
                                   partition: "user:<id>" — never the
                                   global instance's
GET    /term-net?tile=<p>          terminal access on the tile. the network
                                   scopes a terminal there may take for the
                                   caller (D54): {tile, scopes:[{id,label,
                                   desc}], default, label, org, personal} —
                                   `org` where the owning org has network
                                   sets, `personal` where a personal tile's
                                   owner has network sets (D88 — added to
                                   internet, not replacing it), `set:<name>`
                                   for each set the caller may pick (the
                                   owner's sets; every set for a workspace
                                   admin, D65), internet/host where
                                   allowed, offline always. The
                                   picker offers exactly this list; default
                                   is never a set
GET    /logs?component=<p>         admin, the tile itself, or a user with
                                   TERMINAL-level access on it (read/write
                                   users don't — output can carry secrets).
                                   text/plain tail of the backend's captured
                                   stdout/stderr (.xbin/log/<key>.log, all
                                   generations). ?tail=<bytes> (default 64K,
                                   max 1M) sizes it; ?follow=1 keeps streaming
                                   appended bytes (chunked) until the client
                                   goes away (sent with X-Content-Type-Options:
                                   nosniff, so a client doesn't hold back the
                                   first bytes to sniff them). The HTTP twin of `bx logs [-f]`
                                   (which streams it in an isolated terminal,
                                   where .xbin is masked); the terminal
                                   window's read-only logs tab. A failed
                                   deploy of a checkpoint appends
                                   "--- deploy of c:<id> failed <time> ---"
                                   and the compiler output.
                                   &deployment=<name>: one deployment's log
                                   (default: the caller's bound deployment —
                                   a frame's or backend's, a session's named
                                   target — else the primary). A tile's
                                   frames and backend read only their own
                                   (403 "a tile's own credentials act only on
                                   their own deployment (<bound>)"); the
                                   tile's terminal/agent tokens, people with
                                   terminal level and admins read any. 404
                                   unknown, 400 malformed, and 400 for a
                                   component= that names a deployment as
                                   tile+name (D127j). X-XBin-Deployment:
                                   <name> on a non-primary answer and on every
                                   answer to a request that named a
                                   deployment (the echo of a text/plain
                                   answer). A deployment beyond main keeps
                                   its log inside xbind's state
                                   (.xbin/deploy/<tile-key>/d/<name>/
                                   backend.log), with its setup output.
                                   A partitioned tile (docs/partitions.md
                                   §Logs and status): the caller's own
                                   partition's log at any level — a
                                   person's session, their frames,
                                   terminals and agent sessions, the
                                   partition's backend
                                   (.xbin/partition/<tile-key>/<dep>/<pkey>/
                                   backend.log); &user=<id>: that person's,
                                   for them, or for an admin or a tile
                                   manager in their own session while the
                                   person shares it (POST
                                   /partitions/share-log; else 403);
                                   &xbin-partition=global: the global
                                   instance's under the rule above (a
                                   person's partition 403); a credential
                                   acting in no person's partition (the
                                   root token, another tile) reads the
                                   global instance's as before;
                                   &partition= 400. X-XBin-Partition names
                                   the partition served. A follow of a
                                   partition's log asks again every few
                                   seconds and ends (with a closing line)
                                   once the reader may no longer read it —
                                   a share that ended or was revoked
GET    /auth-overview              admin. components(+roles/uses/vault, vm?:
                                   {memMiB?,vcpus?} when the manifest asks for
                                   a VM), grants, pending, counts — powers the
                                   admin console
GET    /vaults                     admin. [{component, keys}] across all vaults.
                                   A partitioned tile's row is its global
                                   instance's keys plus partitions: how many
                                   people's partitions keep a vault — a
                                   count, never their key names (absent when
                                   none)
GET    /resources                  admin. declared resources [{id,scope,name,type}]
GET    /components                 any. [{path, scope, runtime, hasIndex,
                                   state? (lifecycle; absent = enabled),
                                   roles, uses, deps, manifestError,
                                   chrome? (trusted chrome — bx-frame does
                                   not sandbox these: root, shell, shipped
                                   tiles/organisations, admin-approved tiles),
                                   chromeRequested? (the manifest says
                                   chrome: true but no workspace admin
                                   approved it — it runs sandboxed; D118),
                                   sandbox? (extra iframe/CSP sandbox tokens
                                   the tile's grants unlock — cap:open-links
                                   → allow-popups allow-popups-to-escape-
                                   sandbox, ND11; bx-frame appends them),
                                   native? ({entry}: the tile's native app
                                   UI module, tile-relative — its runtime
                                   document is /c/<path>/?native=1; absent
                                   when none, and on chrome),
                                   origin? (--tile-assets=origins only: the
                                   tile's own origin, e.g.
                                   https://t-<id>.tiles.example.com — bx-frame
                                   frames the tile's workspace URL, which the
                                   server sends there, with allow-same-origin
                                   and no credentialless, and talks to it with
                                   that postMessage origin)}]
                                   The entry of a tile with deployments (a
                                   deployment record: live reload paused, or
                                   a deployment beyond main) gains
                                   deployments: {primary, pinned, protected},
                                   the same for every caller who sees it
                                   (pinned: its primary doesn't follow the
                                   work tree; no other deployment's name, no
                                   count); runtime, hasIndex, native, chrome
                                   and template then describe the primary's
                                   code (its checkpoint while pinned), roles,
                                   uses, deps and manifestError the work
                                   tree, and origin is the primary's.
                                   Deployments are never rows.
                                   The entry of a tile whose primary's code
                                   asks for a "partition", or whose
                                   recorded partition mode has one, gains
                                   partition: {state: partitioned |
                                   unpartitioned | pending | invalid, user,
                                   global (the recorded mode), request?:
                                   {user, global, declined} (what the code
                                   asks when it differs: pending, or kept
                                   by a tile manager — declined), note?
                                   (while pending: the code's
                                   partitionNote, trimmed — the tile's own
                                   words, sandbox-writable: show it as
                                   text, attributed to the tile)} and, for
                                   an invalid request, partitionError (also
                                   in manifestError). A tile with a
                                   recorded mode whose code's xbin.json, or
                                   whose mode record, can't be read is
                                   invalid too, with partitionError saying
                                   so (user and global: the recorded mode,
                                   both false when the record can't be
                                   read). Both are absent for every other
                                   tile
GET    /components/<path>          any. {component, apiDoc: <API.md text>}
                                   (component as above, native included)
GET    /tile-assets                any (read-filtered); ?component=<p> for one.
                                   The tile asset report:
                                   per sandboxed tile, what strict tile asset
                                   gating refuses — {mode, tiles:[{component,
                                   injectFalse?, files, findings:[{file, line,
                                   col, kind: html-attr|importmap|css-url|
                                   js-import|js-string|inject-false|base-tag|
                                   symlink-escape, ref, target, breaks:
                                   ""|tokens|strict, fix (the relative URL bx
                                   fix assets writes; "" = by hand), note}],
                                   truncated?, breaking:{tokens, origins}}]}.
                                   Only tiles with findings unless
                                   ?component= (404 if unknown/unreadable).
GET    /frame-token?component=<p>  a principal that may use the tile: humans
                                   (cookie) any tile they can read; a tile
                                   frontend its OWN component — including
                                   cookie-less (sandboxed frames renew with
                                   their token alone); never a backend's
                                   instance token (403: a token with no
                                   person behind it would read as the
                                   owner's frame). {token} — bound to
                                   the caller's login (a renewal keeps its
                                   token's binding; see Authentication).
                                   &deployment=<name>: a person asks for a
                                   deployment's token (write on the tile for a
                                   non-primary one, checked at every
                                   renewal); a tile renews only its own bound
                                   deployment's, and the renewal keeps it.
                                   Without it a person gets the primary's
                                   token (a six-field one when the primary
                                   isn't main); a session that follows the
                                   primary gets the current primary's. The
                                   answer echoes deployment when one was
                                   named. 400 a malformed name, and a
                                   component= that names a deployment as
                                   tile+name (D127j); 403 "deployment URLs
                                   need write access on <tile>" for a
                                   non-primary deployment and a caller
                                   without write, whether or not it exists
                                   (a frame bound beyond the primary whose
                                   user no longer writes the tile
                                   included), and "a tile's own credentials
                                   act only on their own deployment
                                   (<bound>)"; 404 unknown
POST   /path-tickets              a tile's page (its frame token) only.
                                   {path: "<prefix>"} → {url: "/api/~<ticket>/",
                                   expires (unix ms)}: a path ticket (D135,
                                   below) to a prefix of the page's own
                                   tile's API. The prefix is a relative path
                                   of unreserved characters (A–Z a–z 0–9
                                   . _ ~ -), no . or .. segment, ≤ 256 bytes
                                   (400 otherwise); anyone but a tile's page
                                   is 403. The url is used as
                                   /api/~<ticket>/<rest> (§Path tickets)

GET    /alerts                    any. workspace health {alerts:[{level,kind,
                                   tile?,message,system}]} — disk quota / low
                                   disk / cgroup at-limit; system alerts to all,
                                   tile alerts to admins + that tile's users.
                                   An unreadable data/workspace-policies.json
                                   is kind `policies`, admins only.
                                   An alert about a deployment's data beyond
                                   the primary's main carries deployment (the
                                   data's deployment; tile is then the data's
                                   scope, or absent for main while main isn't
                                   the primary): admins only. A limit alert
                                   names the deployment whose cgroup leaf hit
                                   its cap; a tile's checkpoint and build
                                   storage warns at 90% of its quota. Kind
                                   backup-keys (admins only, warn): N backup
                                   keys aren't in any exported key bundle
                                   yet, or the vault passphrase changed
                                   after the last export (GET /backup-keys;
                                   cleared by POST /backup-keys/export).
                                   Kind partition-switch (warn, tile): a
                                   partition mode switch is requested for
                                   the tile, which doesn't run until a tile
                                   manager switches or keeps the current
                                   mode (POST /partitions/mode) — admins and
                                   the tile's readers; it goes when the
                                   request is decided or withdrawn. Kind
                                   partition-invalid (warn, tile): the
                                   tile's partition request can't run (or
                                   its mode record can't be read), so its
                                   primary doesn't — same audience. Kind
                                   partition-consents (warn, admins only):
                                   person consent records in
                                   data/partitions/consents this xbind
                                   can't read (each named; POST /partitions/
                                   consents)
GET    /whoami                    any. caller identity + permissions; for
                                   users also orgs:[{id,name,level,create,
                                   admin,suspended?,via?,viaGroups?}]
                                   (the self-service membership view), and
                                   tileCreation: any|org-only — the
                                   workspace's tile-creation policy (D52)
                                   — and personalTiles: may THIS caller
                                   (the human behind a tile call too) own
                                   tiles personally, the policy and their
                                   account's noPersonalTiles folded in
                                   (D88; owner pickers adapt to it). A
                                   signed-in non-admin also gets personal:
                                   {sets, netSets, netRules, allow} — their
                                   resolved personal plane. An admin's
                                   view-as session adds impersonatedBy
                                   (owner | admin id) and readOnly:true
                                   (D64; the shell's banner). On
                                   element principals driven by a signed-in
                                   human, `user` reports the driver, SCOPED
                                   by the tile's trust (docs/auth.md): every
                                   tile gets {id,name}; a tile inside an org
                                   additionally gets that one org's
                                   membership slice; a workspace-management
                                   tile (xbin / xbin:users capability) gets
                                   {admin, orgs:[…]} in full — the element's
                                   own privilege is unchanged either way.
                                   Every caller also gets native:
                                   {runtime: 1} — this xbind serves native
                                   runtime documents (/c/<tile>/?native=1)
                                   — or {runtime: 0, disabled: true} while
                                   an admin has turned native tile UIs off
                                   (PUT /native-runtime): the app opens
                                   every tile as its web page.
                                   A tile credential bound to a deployment
                                   other than the primary also gets
                                   deployment, its name (a session that
                                   follows the primary gets none)
GET    /openapi.json              any. OpenAPI 3.1 spec of this built-in API,
                                   incl. the RBAC capability per endpoint
                                   (x-xbin-capability). Rendered by the API-docs
                                   tile; importable into Swagger UI / Postman.
POST   /impersonate               admin. {user} → {url, user, expiresIn}:
                                   a one-shot ticket (2 min) to VIEW the
                                   workspace as that user (docs/auth.md
                                   §Viewing the workspace as a user, D64).
                                   Open url top-level in the same browser —
                                   it swaps the cookie for a read-only
                                   session as the user (impersonatedBy in
                                   /whoami and /sessions; every write 403,
                                   terminals too). Bound to the minting
                                   admin's browser; not yourself, not a
                                   disabled account, no nesting
POST   /impersonate/stop          a view-as session. ends the view, hands
                                   the browser back to the admin's own
                                   session → {ok, restored} (restored:false:
                                   it had expired — sign in again). POST
                                   /logout from a view does the same

GET    /term/sessions             authenticated. the caller's live terminal
                                   sessions — the session directory (D73):
                                   [{id,cwd,net,label,scopes,gpu,api,name,
                                   created,lastActive,clients,envHeld,vm}], oldest
                                   first (agent rows add kind:"agent",
                                   provider, mode, model?, status, pending
                                   — unanswered permission requests — and
                                   questions — unanswered elicitations);
                                   ?cwd=<p> one tile only. [] without
                                   terminal rights; a tile the caller may no
                                   longer open a terminal on is omitted.
                                   ?user=<id> admin: another user's. Every
                                   browser the user signs into sees the same
                                   tabs (the shell's <bx-frame> lists them here,
                                   not in the browser). A row whose session
                                   targets a named deployment carries
                                   deployment. A row of a session on a
                                   partitioned tile carries partition
                                   ("user:<id>" | "global": what it acts
                                   in); in an admin's ?user= listing, and
                                   in an admin's view as its person, a
                                   person's session there has an empty
                                   name
PATCH  /term/sessions/<id>        creator or admin (on a partitioned tile
                                   the creator only: 403 for an admin on
                                   another person's session). {name}: name
                                   the tab (empty clears; lives on the
                                   session → follows the user) → ok
GET    /agent/providers           authenticated. the coding agents this daemon
                                   runs: [{id,name,modes:[{id,name,explicit?}],
                                   defaultMode,login}] (D74; explicit modes are
                                   never defaults; login is the shell command
                                   that signs the CLI in — the agent uses the
                                   session's $HOME, no vault key)
POST   /term/sessions             terminal-level on the tile (a shell's own
                                   terminal token counts; an agent
                                   sandbox's never — 403). {cwd, kind:"agent",
                                   provider, mode?, model?, options?, net?,
                                   api?, gpu?, name?, resume?} → SessionInfo
                                   (kind agent, status starting): an AGENT
                                   SESSION — the tile's sandbox runs the
                                   provider's ACP adapter instead of a shell.
                                   resume: a past session id (GET
                                   /agent/history) to reopen — provider, mode
                                   and name carry over, the agent replays the
                                   earlier turns (session/load); 409 when it
                                   cannot (start a new one). 400 unknown
                                   provider/mode, 403, 409 per-user limit,
                                   503 vault sealed / no bx. net/api/gpu:
                                   the sandbox pickers a shell's socket
                                   takes (api false = code-only, no
                                   terminal token). Shells still open on
                                   /ws/term.
                                   ?deployment=<name>: the session's target,
                                   fixed for its life (the primary's name
                                   follows the primary; a protected primary
                                   403, an unknown one 404, not a name 400).
                                   SessionInfo echoes deployment; no echo
                                   means this xbind can't target deployments.
                                   Without it the session follows the
                                   primary — or, while the primary is
                                   protected, targets the live reload target,
                                   and has no tile API (api false) when
                                   there is none (§/ws/term)
GET    /term/sessions/<id>        creator or admin → {session, permissions:
                                   [{pid,toolCall,options}], elicitations:
                                   [{eid,toolCallId?,message,schema}]}
                                   (either kind; the unanswered permission
                                   requests and questions, oldest first —
                                   the elicitation.request payloads)
DELETE /term/sessions/<id>        creator or admin → 204 (either kind; the
                                   API twin of DELETE /ws/term?session=)
POST   /term/sessions/<id>/prompt creator or admin. {text, attachments?:
                                   [{name, mime?, data}]} → {ok, turn};
                                   409 while a turn runs or another prompt
                                   is being taken (before this body is
                                   read), and when a cancel lands while its
                                   files are handed over (no turn starts).
                                   attachments (optional; text may then be
                                   empty): files, data standard base64 — at
                                   most 10, each ≤ 10 MiB, 20 MiB together
                                   (413 past a limit; the body is capped at
                                   32 MiB). Each is written inside the
                                   agent's sandbox (its private /tmp; with
                                   isolation off a directory xbind makes
                                   and removes with the session — the
                                   newest 64 MiB kept) and handed to the
                                   agent by that path. A png/jpeg/gif/webp
                                   (the bytes decide, not mime) also goes
                                   inline as an image the model sees —
                                   400 when the agent does not take images
                                   (it did not advertise
                                   promptCapabilities.image) — when it is
                                   ≤ 3.75 MiB (5 MiB as base64, the
                                   strictest model API's limit) and the
                                   prompt's inline images stay ≤ 4 MiB
                                   together; past either it is only a file
                                   the agent opens with its tools. The
                                   agent keeps inline images in its
                                   conversation and resends them every
                                   turn, so downscale photos (≤ 2576 px
                                   on the long edge is all a model reads)
                                   and convert HEIC to JPEG (other image
                                   types are files). A text file ≤ 128 KiB
                                   also goes inline as an embedded
                                   resource when the agent takes embedded
                                   context. The log records the names,
                                   types, sizes and whether each went
                                   inline, never the bytes
POST   /term/sessions/<id>/restart
                                   the creator. {net?, api?, gpu?} →
                                   {session, resumed}: the sandbox pickers
                                   are fixed at start, so the session ends
                                   and a new one opens — same tile,
                                   provider, mode, settings, name —
                                   resuming the conversation where the
                                   agent can (session/load; resumed true,
                                   its history entry superseded), else
                                   fresh (the transcript stays in
                                   /agent/history).
                                   ?deployment=<name>: restart onto that
                                   target (the primary's name follows the
                                   primary; 403 a protected primary, 404
                                   unknown, 400 not a name — a refused
                                   target leaves the session running); the
                                   new SessionInfo echoes deployment
POST   /term/sessions/<id>/cancel creator or admin → ok (the turn ends
                                   cancelled; pending permissions cancelled;
                                   a prompt still handing its files over
                                   gets 409 and never starts)
POST   /term/sessions/<id>/permissions/<pid>
                                   creator or admin. {optionId} | {decision:
                                   allow_once|allow_always|reject_once|
                                   reject_always} → ok; the first answer
                                   wins (404 after); allow_always records a
                                   session rule (nothing in xbin.json) —
                                   never for kind switch_mode (a plan
                                   approval: its allow_always options are
                                   modes), which is always asked
POST   /term/sessions/<id>/elicitations/<eid>
                                   creator or admin. {action: accept|
                                   decline|cancel, content?} → ok: answer
                                   a question the agent asked
                                   (elicitation.request); content = the
                                   form's values (an object keyed by the
                                   schema's properties) with accept; the
                                   first answer wins (404 after)
GET    /term/sessions/<id>/events creator or admin. ?since=<seq> → {events,
                                   next, truncated}; ?follow=1 streams NDJSON
                                   from the cursor until the client or the
                                   session goes (§Agent session events); its
                                   head is sent at once, before any event.
                                   ?before=<seq>&limit=<n> (either one) → a
                                   page instead: {events, hasOlder,
                                   nextBefore, truncated, next, last, state}
                                   — the tail (no before) or the events
                                   before a seq, cut where a fold may start
                                   (§Agent session events → Pages)
POST   /term/sessions/<id>/options
                                   creator or admin. {id, value}: change a
                                   setting the agent advertised (model,
                                   effort, mode, … — the `options` list on
                                   the session's idle status event); applied
                                   to the next turn, the refreshed list rides
                                   the next status event → ok
GET    /term/sessions/<id>/log    creator or admin. text/plain: the adapter's
                                   stderr + driver notes (debugging)
GET    /term/sessions/<id>/diff   creator or admin. ?toolCallId=<id> |
                                   ?turn=<n>, ?path=<file>? → text/x-diff:
                                   the complete git patch behind a
                                   files.changed event (whose own patch is
                                   capped), or one file of it — up to 16
                                   MiB (X-Truncated: true when cut). Edit
                                   calls (which report their own diff) have
                                   one too. 404 when the session keeps no
                                   snapshot for it (nothing changed, not a
                                   git tile, or the session ended). One
                                   diff runs at a time per session and two
                                   across xbind: a request past that waits
                                  (prompt, permissions, elicitations,
                                   options, restart and diff answer 403 to
                                   any agent session's own terminal token —
                                   an agent sandbox's XBIN_TOKEN, on every
                                   session, its own or another's: an agent
                                   never answers its own requests or drives
                                   itself, not through a sibling either; a
                                   shell's token on the tile still does,
                                   as `bx agent` there)
                                  (on a partitioned tile — docs/partitions.md
                                   §Terminals and agent sessions — "or
                                   admin" holds for the admin's own
                                   sessions only: on another person's
                                   session every route above but DELETE
                                   answers 403 `session belongs to another
                                   user: <tile> keeps each person's data
                                   apart, so an admin can't open other
                                   people's sessions there (ending one is
                                   allowed)`, and an admin viewing as a
                                   person gets 403 `viewing as someone
                                   opens no session on <tile>: it keeps
                                   each person's data apart` on every GET
                                   above, the person's own sessions
                                   included. A new session there acts in
                                   its opener's partition — SessionInfo
                                   carries partition — or, without a
                                   person, in global (api false when the
                                   tile has none); 409 while the tile's
                                   partition mode switches or the
                                   opener's partition of it is being
                                   reset or removed. resume answers 409
                                   for a past session of the other store:
                                   a partition's entry continues only in
                                   its person's session on the
                                   partitioned tile, a person's own entry
                                   only outside one. A person's terminal
                                   on the tile may open and drive its own
                                   agent sessions)
GET    /agent/history             terminal-level. Your past agent sessions,
                                   newest first: [{id, cwd, provider, mode,
                                   name, created, ended, turns, preview,
                                   loadable}] — the transcripts kept when a
                                   session ended or the daemon stopped (per
                                   user × tile, the newest 20 per tile;
                                   never-prompted sessions are not kept).
                                   ?cwd= narrows to a tile. loadable: the
                                   agent can reopen it (resume). An entry of
                                   a session that had a named target carries
                                   deployment. A session on a partitioned
                                   tile's entry is kept with its person's
                                   partition, and listed, read and deleted
                                   here the same way — never by a person
                                   recreated under the same id, nor by an
                                   admin viewing as the person (404)
GET    /agent/history/<id>/events terminal-level (own). {meta, events} — the
                                   persisted transcript in the live /events
                                   shape; 404 when not yours or gone.
                                   ?before=&limit= → {meta, …a page} as on a
                                   live session
DELETE /agent/history/<id>        terminal-level (own) → 204 (forget it)
GET    /prefs                     the caller's per-(user×tile) prefs object
GET    /prefs/<key>               one pref value (arbitrary JSON) | 404
PUT    /prefs/<key>               set it (body = JSON value)
DELETE /prefs/<key>               remove it
                                  (each principal reads/writes only its own
                                   bucket; the shell stores layout here; a
                                   tile credential acting in a deployment
                                   beyond main keeps a bucket per user and
                                   deployment, which doesn't follow a
                                   reassignment of the primary. Writes to
                                   one bucket are serialised — concurrent
                                   writes of different keys all land. A
                                   successful PUT/DELETE publishes a
                                   `prefs` event to the bucket owner's own
                                   clients; an optional request header
                                   `X-Prefs-Writer: <id>` (≤64 chars) is
                                   echoed in it as data.writer, so a client
                                   can skip its own writes)
GET    /users                     admin or xbin:users. [{id,name,role,
                                   tiles:{path:level}, termApi, termNet,
                                   canCreate (deprecated, ignored — D82),
                                   noPersonalTiles?, noTerminal?, sets?,
                                   netSets?, personal? (the resolved plane
                                   for non-admins: {sets, netSets,
                                   netRules, allow} — D88),
                                   disabled?, invitePending?,
                                   deviceCount? (enrolled app devices),
                                   email?, roleVia?, lastLogin?,
                                   lastLoginVia?, lastSSO?, ssoGroups?,
                                   ssoSyncError?}] — levels
                                   read|write|terminal (docs/auth.md, D16).
                                   Sign-in facts (D53): lastLogin (unix) +
                                   lastLoginVia (password|invite|sso|
                                   device); lastSSO (unix) = the last SSO
                                   sign-in;
                                   ssoGroups = the IdP groups seen at the
                                   last SSO sign-in; ssoSyncError = the
                                   last group-fetch failure; roleVia "sso"
                                   = admin by group rule
POST   /users                     admin/xbin:users. create a user: {id,
                                   name?, role?, email?, tiles?,
                                   termApi?, termNet?, password? | sso?,
                                   orgs?: [{org, level, create, admin}],
                                   noPersonalTiles?, noTerminal?, sets?,
                                   netSets?}. The personal plane (D88):
                                   unknown sets refuse before anything is
                                   created; the switches are OR'd with the
                                   seed's (a request can't lift them).
                                   canCreate? is still accepted (and
                                   PATCH-able) but deprecated and ignored
                                   (D82). orgs joins the account at creation
                                   (validated first — a bad org creates
                                   nothing; D53) → response adds orgs.
                                   Under SSO-only mode a non-admin needs
                                   sso:true or a password (409 otherwise).
                                   WITH password → ready to sign in;
                                   WITHOUT → credential-less account + a
                                   single-use invite link the admin
                                   delivers: {user, invite, inviteUrl:
                                   /login?invite=…, inviteExpires} (72h,
                                   D22); sso:true (email required) →
                                   credential-less with NO invite — the
                                   bound email's IdP sign-in is the
                                   credential (SSO pre-provisioning, D52).
                                   Every new account is seeded with the
                                   new-account defaults (/defaults
                                   newUsers) on top of the given fields.
                                   There is NO self-signup — accounts only
                                   come from here. (id: [a-z0-9._-],
                                   immutable; password ≥ 8; the legacy body —
                                   tiles array + terminal bool — is still
                                   accepted and migrated)
POST   /account/password          signed-in users. {current, new,
                                   removeDevices?} — self-service rotation;
                                   verifies the current password (D38).
                                   removeDevices:true also removes the
                                   caller's app devices (all but the one
                                   calling) → {ok, devicesRemoved}
POST   /login                     none — the password is the credential.
                                   The native app's sign-in: {username,
                                   password} → the app token response
                                   ({token, tokenType, user, expiresIdle,
                                   expiresMax}, as POST /login/device).
                                   Same rules as the form login: throttled,
                                   disabled accounts refused, SSO-only mode
                                   (D53) refuses non-admins (403)
GET    /login/methods             none — public, not throttled. The
                                   native app's sign-in discovery: what
                                   the login page offers, as data →
                                   {api:1, title, auth, password:{enabled,
                                   adminOnly}, sso:{enabled, label},
                                   invites}. title: the branding title,
                                   else "xbin". auth:false in no-auth
                                   mode (everything else off then).
                                   password.enabled: the workspace has
                                   accounts; adminOnly: SSO-only mode
                                   (D53). sso.enabled: the login page's
                                   SSO button shows (configured and
                                   --external-url set), label its text.
                                   invites: invite links can be redeemed
                                   (below). Cache-Control: no-store; api
                                   versions the shape (fields only added)
POST   /invite/check              none — the invite is the credential.
                                   {invite} (the token of an invite link
                                   <origin>/login?invite=<token>) →
                                   {user:{id, name}, expires, title}: whom
                                   it is for, without spending it. 403
                                   {error:"invalid or expired invite"}:
                                   unknown, used, expired or a disabled
                                   account's (throttled; failures count)
POST   /invite/redeem             none — the invite is the credential.
                                   {invite, password} → the app token
                                   response (as POST /login): the invite
                                   form (POST /login/invite) for the app —
                                   sets the first password, spends the
                                   single-use invite, signs in (Via
                                   "app", last sign-in via "invite";
                                   audit-logged). 400 {error}: the password
                                   fails the policy (min 8 characters) —
                                   the invite is NOT spent; 403 as above;
                                   409 {error: "waiting for <person> to
                                   confirm…"}: a link held for its person
                                   (docs/partitions.md §Credential resets;
                                   stored held, so an xbind without the
                                   check never redeems it),
                                   not spent; 429 throttled
POST   /devices/enroll-code       a signed-in user (browser or app
                                   session), [{password}]. → {code, url:
                                   "xbin://enroll?u=<origin>&c=<code>",
                                   origin, expires}: a one-time code
                                   (5 min) enrolling ONE device for the
                                   caller — the shell shows url as a QR
                                   code (or the same link with u= an
                                   address the user typed for the phone;
                                   the device still signs origin).
                                   origin: the --external-url
                                   origin, else the request's scheme://host.
                                   Step-up: the caller's sign-in must be
                                   under 10 min old, or the body carries
                                   their password; else 403 {error,
                                   stepUp: "password" (resend with it) |
                                   "signin" (sign in again: SSO account or
                                   SSO-only mode)}. Wrong passwords are
                                   throttled
POST   /devices/enroll            none — the code is the credential.
                                   {code, name, platform, publicKey (SPKI
                                   DER of an EC P-256 key, base64url)} →
                                   {deviceId, user, origin, name}. 401
                                   bad/spent/expired code (throttled), 400
                                   bad key, 409 past 32 devices per user
POST   /web-ticket                the app's device-key session (via
                                   device) only — not a browser, the
                                   app's password/SSO session, a tile, a
                                   terminal or the owner token (403).
                                   [{next}] → {url: "<device origin>/
                                   login?ticket=<t>&next=<path>", expires,
                                   expiresIn: 60}: a signed-in web view —
                                   the app opens url top-level (a chrome
                                   tile's own web view); GET /login?
                                   ticket= (Core) shows a signed-out
                                   browser "Continue as <name>", and its
                                   button (POST /login/web-ticket) signs
                                   that browser in as the same user,
                                   landing on next. next: a
                                   path on this workspace (one leading
                                   slash, printable ASCII, no backslash,
                                   ≤ 2048, not /login… or /logout;
                                   default /), else 400. The
                                   ticket is one-shot (60 s) and bound to
                                   the device session: the app signing
                                   out, removing the device, sign-out-
                                   everywhere and disabling void it. 403
                                   {reauth:"sso"} for an SSO-bound account
                                   past its window (D93); 10 per minute
                                   per device (429 + Retry-After); audited
GET    /devices                   a signed-in user. {devices: [{id, name,
                                   platform, origin, created, lastUsed,
                                   lastIP, current}]} — own app devices;
                                   current marks the device behind the
                                   calling app session
DELETE /devices/<id>              the device's user, or admin/xbin:users
                                   → {ok, user, dropped}: removes the key
                                   and ends every session it opened (and
                                   the frame and asset tokens they minted)
                                   and its push registration
GET    /users/<id>/devices        admin/xbin:users. {devices: […]} as
                                   GET /devices (no current)
GET    /screens                   signed-in. {default: {tiles}|null, org:
                                   [{id,org,name,edit,tiles,rev,updatedBy,
                                   updatedAt,canEdit}], folders: {"ws":
                                   {folders,rev,updatedBy,updatedAt,canEdit},
                                   "org:<id>": {…}}} — the ws default
                                   screen, your orgs' screens, and the
                                   shared sidebar folder sets you may see
                                   (ws always; each org you belong to;
                                   ws-admins: every org) (D37/D55)
PUT    /screens/default           ws-admin. {tiles} — the seed screen new
                                   users start from
PUT    /screens/org               {id?,org,name?,edit?,tiles?,rev?,force?}
                                   — create (no id; org admin) → {ok,id,
                                   rev:1,…}; a tiles update follows the
                                   screen's edit knob (admins|write|
                                   members) and carries `rev`, the
                                   revision it was based on: stale → 409
                                   {error, rev, screen} unless force:true;
                                   no rev = legacy overwrite. No tiles =
                                   meta-only (name/edit; org admin), which
                                   never bumps the revision (D55)
DELETE /screens/org               org admin. {id, org}
PUT    /screens/folders           {scope:"ws"|"org:<id>", folders:[…], rev,
                                   force?} — replace one owner section's
                                   curated sidebar tree (ws: ws-admin;
                                   org: its admins). rev 0 for a scope's
                                   first save; stale → 409 {error, rev,
                                   folders}; folders:[] clears (D55)
POST   /users/<id>/invite         admin/xbin:users — or an ORG ADMIN for a
                                   non-admin member of their org
                                   (delegated reset-by-link, D38).
                                   (re)mint an invite link
                                   for an existing user — credential delivery
                                   or reset-by-link; re-minting invalidates
                                   the previous link; the current password
                                   keeps working until redemption. → {invite,
                                   inviteUrl, inviteLink (absolute, from the
                                   request host), inviteExpires}. 409 for a
                                   non-admin under SSO-only mode (D53).
                                   For a person who holds partitions
                                   (docs/partitions.md §Credential resets)
                                   the link is audited and the person told
                                   (push account.credential, a notice);
                                   with the workspace policy
                                   credentialResetConfirm on it is minted
                                   held — the answer adds held: true,
                                   heldUntil — and its redemption answers
                                   409 "waiting for <person> to confirm…"
                                   (the web form shows it) until the person
                                   allows it or 24 h after they were told
PATCH  /users/<id>                admin/xbin:users. update — present fields
                                   overlay (+password reset). {disabled:
                                   bool} suspends/restores the account
                                   (D34): login, sessions, tokens and
                                   invite redemption refuse while set;
                                   rows/memberships/ownership stay.
                                   Guarded: not yourself, not the last
                                   enabled admin. The personal plane
                                   overlays by presence (D88):
                                   noPersonalTiles, noTerminal (switching
                                   it on ends the user's live terminal and
                                   agent sessions), sets, netSets (a change
                                   restarts their net tiles). A password or
                                   SSO email set for someone else who holds
                                   partitions is audited and they are told;
                                   with credentialResetConfirm on it is held
                                   instead of applied (the old one keeps
                                   working) and the answer carries
                                   X-XBin-Credential-Held: password,email
                                   (docs/partitions.md §Credential resets).
                                   Disabling a person (or a change that
                                   takes their read on a tile) stops their
                                   running partition instances
DELETE /users/<id>                admin/xbin:users. remove (revokes
                                   sessions) → {ok, orphanedTiles: […]} —
                                   tiles that fell to workspace-owned, so
                                   the handover is explicit. A partition
                                   holder's instances stop and their
                                   partitions are orphaned (swept after 30
                                   days, or purged); their personal binds,
                                   consents, notices and held credentials
                                   go; homes/<id> and data/agent-history/<id>
                                   move to data/orphans/<id>-<uid8>/
DELETE /users/<id>/sessions       admin/xbin:users. "sign out everywhere"
                                   (D53): ends every browser and app
                                   session, terminal token and frame token
                                   of the user and voids their pending
                                   enrollment codes → {ok, dropped,
                                   devicesLeft, devicesRemoved?}. They can
                                   sign in again — disable the account to
                                   stop that. Enrolled app devices stay
                                   (devicesLeft of them, able to sign in
                                   without a password) unless ?devices=1
                                   removes them too
GET    /sessions                  admin/xbin:users. {sessions: [{user, name,
                                   created, lastActive, ip, lastIP,
                                   current, impersonatedBy?, via,
                                   device?}]} — live login sessions (via:
                                   session = a browser, device = the
                                   native app signed in with a device key
                                   — device names it — app = the app's
                                   password/SSO sign-in; a browser the app
                                   signed in (POST /web-ticket) is session
                                   with device set, created = the device
                                   login's; impersonatedBy: an
                                   admin's read-only view of the user, D64) with
                                   client IPs (login IP + last-seen IP),
                                   newest activity first; the caller's own
                                   row is marked current:true. Session ids
                                   are credentials and never returned.
                                   Stateless bootstrap token logins don't
                                   appear. This is the attribution view for
                                   the /c/ warm-IP gate (admin console →
                                   user management → sessions)
GET    /auth-settings             admin/xbin:users. {tokenLoginDisabled,
                                   hasAdminUser, canDisable,
                                   passwordLoginDisabled,
                                   canDisablePassword, sso: {enabled,
                                   ready, kind, preset, issuer, clientId,
                                   clientSecretSet, allowedDomains,
                                   buttonLabel, externalUrl, groupsClaim,
                                   groupsScope, adminGroups, groupSync:
                                   {rulesActive, knownGroups, lastError:
                                   {user, at, error}|null}}} — sign-in
                                   policy (docs/auth.md §SSO): the SSO
                                   config with the secret reduced to a
                                   bool, SSO-only mode (D53), and the
                                   group-sync status (every group the IdP
                                   has been seen sending, the newest
                                   recorded fetch failure)
POST   /auth-rotate-token         admin. Rotate the owner token: rewrites
                                   .xbin/token, old token dies immediately
                                   (bearer + cookie), with the frame tokens
                                   it opened and the owner's push
                                   registrations. → {token} (shown once).
PATCH  /auth-settings             admin/xbin:users. {tokenLoginDisabled?:
                                   bool, sso?: {kind, preset, issuer,
                                   clientId, clientSecret, allowedDomains,
                                   buttonLabel, groupsClaim, groupsScope,
                                   adminGroups}|null,
                                   passwordLoginDisabled?: bool}. Disabling
                                   token login requires a signed-in admin
                                   user (Bearer owner token unaffected); an
                                   sso object replaces the config (empty
                                   clientSecret keeps the stored one), null
                                   clears it; passwordLoginDisabled = SSO-
                                   only mode for non-admins — enabling
                                   needs a READY provider (409 otherwise).
                                   A new provider identity (kind, issuer,
                                   client id) is a credential for every
                                   partition holder bound by email: audited
                                   naming them, each told; with
                                   credentialResetConfirm their SSO sign-ins
                                   are held (?sso_err=held) until they allow
                                   it or 24 hours pass (docs/partitions.md
                                   §Credential resets)
POST   /auth-settings/sso/test    admin/xbin:users. probe the provider
                                   without a user (D53): OIDC discovery +
                                   JWKS, or GitHub API reachability. Body
                                   {} = the stored config, {sso: {…}} = an
                                   unsaved draft (empty clientSecret = the
                                   stored one). Always 200 → {ok, kind,
                                   issuer, redirectUri, externalUrl, ready,
                                   endpoints, jwksKeys, warnings, error}

GET    /orgs                      management view (docs/auth.md, ownership):
                                   admin/xbin:users → all orgs; a signed-in
                                   org admin → their orgs. {orgs:[{id,name,
                                   members:[{id,level,create,admin,
                                   suspended?,via?,viaGroups?}],tiles,
                                   sets,allow,policy,ssoGroups:[{group,
                                   level,create,admin}],resolvedAllow,
                                   ownedTiles,netSets:[…],resolvedNet:[…],
                                   netHost?}]}. via "sso" = the row was
                                   created by a group rule (D53) and
                                   follows the IdP; ssoGroups = the org's
                                   rules; netSets = attached network sets
                                   (D54), resolvedNet their rule union,
                                   netHost whether it grants host
                                   networking
POST   /orgs                      admin/xbin:users. create {id, name?}
                                   (id: [a-z0-9._-], immutable; "workspace"
                                   reserved)
PATCH  /orgs/<org>                admin/xbin:users, or that org's admin:
                                   {name?, members?} — members replaces the
                                   whole list (provenance via/viaGroups is
                                   store-owned: kept from the previous row,
                                   ignored in the body); a member entry's
                                   suspended:true pauses the membership
                                   (confers nothing, stays listed; D34).
                                   WS-ADMIN ONLY fields:
                                   {sets?, allow?, netSets?} — delegation
                                   is granted from above (D26/D28); xbin/
                                   xbin:* never valid in allow; netSets
                                   attaches network sets (D54) and
                                   restarts the org's net-declaring tiles
PUT    /orgs/<org>/members/<user> admin/xbin:users, or that org's admin.
                                   upsert ONE membership (D53): {level?,
                                   create?, admin?, suspended?, via?: ""}.
                                   Present fields overlay; a new row starts
                                   at read. via:"" DETACHES a synced row
                                   (manual from then on); any other via is
                                   refused — provenance is written by SSO
                                   sync only. → the org view; 404 unknown
                                   org/user
DELETE /orgs/<org>/members/<user> same gate. remove ONE membership → {org,
                                   removed, note?}; 404 when not a member.
                                   A synced row returns at the user's next
                                   sign-in while its rule stands — note
                                   says so
PUT    /orgs/<org>/sso-groups     admin/xbin:users (ws-admin — rules grant
                                   power). replace the org's IdP-group
                                   rules: {rules: [{group, level?, create?,
                                   admin?}]} — group = OIDC claim value,
                                   GitHub org/team-slug, or Google group
                                   email; matching rules union. Applied at
                                   each member's next SSO sign-in
                                   (docs/auth.md §Group sync, D53) → the
                                   org view
DELETE /orgs/<org>                admin/xbin:users; refused while the org
                                   still OWNS tiles (transfer first)
GET    /permission-sets           admin/xbin:users. {sets:{name:{allow,
                                   policy,termApi,termNet}}, attachedTo:
                                   {name:[orgIds]}, heldBy:{name:
                                   ["user:<id>"|"personal-defaults"|
                                   "new-accounts"]}} (D28, D88)
PUT    /permission-sets/<name>    admin/xbin:users. replace one set (same
                                   allow grammar/floor as org allow)
DELETE /permission-sets/<name>    admin/xbin:users; refused while attached
                                   to any org or held by a user, the
                                   personal defaults or the seed (D88)
GET    /net-sets                  admin/xbin:users. organisation network
                                   sets (D54): {sets:{name:{rules,
                                   created}}, attachedTo:{name:[orgIds]},
                                   boundBy:{name:[tiles bound to
                                   set:<name>, D65]}, heldBy:{name:
                                   [user:<id> | personal-defaults |
                                   new-accounts]} (D88: users' personal
                                   networks)}.
                                   Rules are the net: allowance grammar
                                   without the prefix — internet |
                                   internet:<host|host-glob|ip|cidr>[:port]
                                   | lan:<ip|cidr>[:port] | host |
                                   provider:<tile-glob>. Attached to an
                                   org, the union is at once the CEILING on
                                   its tiles' net bindings, what its admins
                                   may bind without an allowance, the
                                   default binding (`org`) of its tiles
                                   that declare net, and the egress of
                                   terminals opened on them. Held by a
                                   user (or the personal defaults) it is
                                   part of their personal network: the
                                   default (`personal`) of the tiles they
                                   own, a terminal scope there, what they
                                   may bind — no ceiling (D88)
PUT    /net-sets/<name>           admin/xbin:users. {rules:[…]} — create/
                                   replace (400 on grammar; one destination
                                   per rule; globs only in internet: host
                                   rules; lan: rules are addresses/CIDRs);
                                   attached orgs' net tiles and every tile
                                   bound to set:<name> restart →
                                   {name, rules, created, attachedTo}
DELETE /net-sets/<name>           admin/xbin:users; 409 while attached to
                                   an org, bound by a tile (D65), or held
                                   by a user, the personal defaults or the
                                   seed (D88)
GET    /owner?tile=<path>         any principal that can READ the tile.
                                   {tile, owner: "user:<id>"|"org:<id>"|""}
GET    /owner/preview             ?tile=&to= — transfer impact report
                                   (D39, never mutates): {allowed,
                                   callerLevel:{before,after},
                                   deadBindings, deadGrants, planeChanges}
                                   — what dies under the NEW owner's
                                   ceilings and what happens to YOUR level
POST   /owner                     transfer (D24/D39): {tile, to}. GIVE:
                                   ws-admin / the user-owner / an
                                   owning-org admin. RECEIVE into org:X
                                   needs X's Create knob ("transfer into ≡
                                   create in"); user:<self> with the GIVE
                                   right; user:<other> and workspace are
                                   ws-admin acts. Side effects: fully
                                   ceiling-dead binding slots are UNBOUND,
                                   affected backends restart, and the
                                   response carries the executed report
                                   (+unbound). The tile's deployments move
                                   with it: its deployment record's owner
                                   ref is rewritten in the same step, so a
                                   pinned primary stays pinned; a record
                                   that can't be written answers 500 and
                                   nothing moves. A transfer keeps every
                                   deployment, its settings and the edge
                                   policy, and restarts each running one
                                   under the new owner's ceilings
GET    /access?tile=<path>        ws-admin, the tile's USER-OWNER, or an
                                   owning-org admin. {tile, owner, entries:
                                   [{kind:user|org, id, level, source:
                                   exact|pattern:<pat>}]}
PUT    /access                    same gate (sharing is an ownership right,
                                   D24). set/clear one EXACT entry: {tile,
                                   kind:user|org, id, level:
                                   read|write|terminal|""}; kind user also
                                   takes "none" = explicit EXCLUSION. Exact
                                   user entries are AUTHORITATIVE (D31):
                                   they override org level, shares, pattern
                                   entries and defaults — down as well as up
GET    /access/<user>             element: a tile's BACKEND (instance
                                   token), about ITSELF. {user, level:
                                   none|read|write|terminal, active} — the
                                   level X-XBin-User-Level would carry for
                                   that person now; none for a disabled or
                                   unknown account (active: false). For a
                                   credential the tile keeps past a call (an
                                   SSH key registered on its page): is its
                                   person still one of its users? Never
                                   another tile; never 404 (an unknown id is
                                   level none); frame/terminal tokens,
                                   people and admins 403. "user:<id>"
                                   accepted. SDK xbin.AccessOf
GET    /access-matrix             admin/xbin:users. users×components
                                   effective levels with provenance:
                                   {users, components, matrix:{user:{tile:
                                   {level, explain:[{level,source}]}}},
                                   owners} — sources: admin | owner |
                                   org-admin:<org> | exact (authoritative,
                                   D31) | org-member:<org> |
                                   org-share:<org>:<pat> | direct:<pat> |
                                   default:<pat>
GET    /access-requests           signed-in users / admin. pending human
                                   access requests (D36), scoped to the
                                   viewer: {requests: [{user, tile, level,
                                   note?, created, mine?, manage?}]} —
                                   mine = you filed it; manage = you may
                                   approve it (the tile's owner, its org's
                                   admins, or ws-admin)
POST   /access-requests           signed-in users. file {tile, level:
                                   read|write|terminal, note?} (≤20
                                   pending each; same-tile refiles
                                   replace; refuses levels you already
                                   hold, tiles you're explicitly excluded
                                   from (exact `none`), and re-files
                                   within 24h of a manager's dismissal). A signed-in human navigating to
                                   an unreadable /c/ tile gets this as a
                                   page (owner named + one-click request)
                                   instead of a bare 403
POST   /access-requests/approve   the tile's manager set. {user, tile,
                                   level?} — writes the exact ACL entry
                                   (level defaults to the requested one)
                                   and removes the request
DELETE /access-requests           {user?, tile} — withdraw your own (no
                                   cooldown); a MANAGER dismissing someone
                                   else's starts the 24h re-file cooldown
GET    /users-directory           admin/xbin:users, or any org admin. the
                                   minimal people list for pickers:
                                   {users:[{id,name}]} — identity only
GET    /policy                    admin/xbin:users. workspace policy-ceiling
                                   rows {policy:[{tiles,deny?,mayCall?}]}
                                   (deny kinds net|gpu|xbin-caps|ingress)
PUT    /policy                    admin/xbin:users. replace the rows
GET    /orgs/<org>/policy         admin/xbin:users, or that org's admins
                                   (read-only — the ceilings their
                                   approvals can trip on). Rows apply to
                                   tiles the org OWNS
PUT    /orgs/<org>/policy         admin/xbin:users. replace them
GET    /branding                  authenticated. {title, icon, hasIcon} — the
                                   workspace's branding (D76): title replaces
                                   "workspace" in the shell header and the
                                   tab title ("<title> · xbin"); icon is a
                                   data: URI that replaces xbin's mark as the
                                   favicon and logo on the workspace page and
                                   the sign-in / invite pages. Empty = xbin's
                                   own. The shell reads it at boot and on the
                                   `branding` event
PUT    /branding                  admin. {title?, icon?}: each present key
                                   is a whole-value replace ("" clears), an
                                   absent one is left alone. title ≤ 64 chars;
                                   icon a base64 data: URI of image/svg+xml |
                                   png | jpeg | webp | x-icon whose bytes
                                   match, ≤ 256 KiB (body ≤ 512 KiB) → the
                                   full view; publishes `branding`; audited.
                                   Kept in data/branding.json — readable while
                                   the vault is sealed, so the sign-in page
                                   shows it
GET    /native-runtime            authenticated. {enabled, runtime,
                                   version} — the workspace's native-runtime
                                   switch: runtime is whoami's native.runtime
                                   (version while enabled, 0 while off)
PUT    /native-runtime            admin. {enabled: bool} → the same view.
                                   Off: whoami says native {runtime: 0,
                                   disabled: true} (the app opens tiles as
                                   web pages) and /c/<tile>/?native=1
                                   answers 410 with the reason (&preview=1
                                   still served). Kept in users.json;
                                   publishes `native`; audited
GET    /workspace-policies        a person (session, device, or a
                                   terminal or agent session they drive,
                                   any deployment) or admin; other tile
                                   principals (frames, instances, cron, bus)
                                   403.
                                   {schema: 1, partitionConsent,
                                   credentialResetConfirm} — the workspace
                                   policies for partitioned tiles (PD-55),
                                   both off by default. partitionConsent: a
                                   partitioned tile uses another partitioned
                                   tile's data of a person only with that
                                   person's consent; credentialResetConfirm:
                                   an admin-set sign-in link, password or SSO
                                   email for someone holding partitions waits
                                   for them to confirm, or 24 h after they
                                   are notified. Kept in
                                   data/workspace-policies.json (not
                                   users.json, which an older xbind rewrites
                                   without keys it doesn't know), each
                                   switch by its exact key. A file xbind
                                   can't read (not a JSON object, a value
                                   not true or false, a mis-cased key) never
                                   turns a switch off: one it can't read
                                   keeps the last value xbind read, or is
                                   on; GET answers 500 (admins get the
                                   reason) and admins see a `policies`
                                   alert until the file is fixed by hand
PUT    /workspace-policies        admin. {partitionConsent?,
                                   credentialResetConfirm?}: each present key
                                   replaces that switch, an absent one is
                                   left alone (at least one; any other key is
                                   400) → the full view; keeps every other
                                   key of the file; 500 without writing on a
                                   file it can't read; publishes `policies`;
                                   audited with each switch's old→new
POST   /partitions/limits         admin; a tile manager (with their own
                                   session, app or device) may lower their
                                   own tile's. {tile?, maxRunning?,
                                   partitionBytes?}: without tile, the
                                   workspace's cap on people's partition
                                   instances running at once (maxRunning,
                                   admin only; partitionBytes 400); with
                                   tile, that tile's cap and the byte ceiling
                                   of each person's partition of it — an
                                   admin's values, or a manager's lower ones
                                   (above the admin's value or the default:
                                   403). 0 clears a value; maxRunning is
                                   ≤ 4096, partitionBytes ≥ 1 MiB. The caps
                                   default from host memory M (per tile
                                   clamp(M/4 ÷ E, 4, 32), workspace
                                   clamp(M/2 ÷ E, 8, 128), E ≈ 160 MiB; M
                                   is MemTotal, or xbind's own cgroup
                                   memory limit when lower), the ceiling
                                   to the tile's per-namespace one. →
                                   {tile?, limits?:
                                   {maxRunning, partitionBytes}, workspace:
                                   {maxRunning}, defaults: {maxRunning,
                                   workspaceMaxRunning}} (effective values;
                                   partitionBytes 0 = the default). The
                                   tile's own credentials 403, an unknown
                                   tile 404. Kept in
                                   data/partition-limits.json (a file xbind
                                   can't read: the defaults apply and POST
                                   answers 500); applies at the next start;
                                   audited (docs/partitions.md)
GET    /chrome                    admin. {tiles: [{path, requested,
                                   approved, shipped?, chrome, missing?}]} —
                                   every component whose xbin.json says
                                   chrome: true and every approved path
                                   (D118). chrome = effective (runs
                                   unsandboxed); missing = approved, no
                                   component there now. root/shell (implicit
                                   chrome) are not listed
PUT    /chrome                    admin. {path, approved: bool} → that row.
                                   Approving needs a component at the path
                                   and applies while its xbin.json says
                                   chrome: true: its documents then run with
                                   the session cookie, acting as whoever
                                   opens it — approve only a tile whose every
                                   writer you trust like the shell. 400 for
                                   root/shell; 404 approving a missing path.
                                   Kept in users.json (a leftover a new tile
                                   at the path would inherit); publishes
                                   `grants` for the tile; audited
GET    /defaults                  admin/xbin:users. {defaultTiles:
                                   {pattern: level}, newUsers: {tiles,
                                   termApi, termNet, orgs: [{org, level,
                                   create}], noPersonalTiles, noTerminal,
                                   sets, netSets, canCreate (deprecated,
                                   ignored — D82)}, tileCreation:
                                   any|org-only, personalDefaults: {sets,
                                   netSets}}. personalDefaults = the live
                                   personal plane every non-admin gets on
                                   top of their own sets, for the tiles
                                   they own (D88; a network change
                                   restarts every personal net tile). defaultTiles = the live
                                   visibility baseline every user gets
                                   (D27). newUsers = what every NEW account
                                   starts with, copied onto the row at
                                   creation — admin-added, invited, or SSO
                                   auto-provisioned — as a UNION with the
                                   request (never admin; D52). tileCreation
                                   = whether non-admins may own tiles
                                   personally — the knob that restricts
                                   personal creation (org-only: they may only
                                   create org-owned tiles — an unspecified
                                   owner resolves to their single Create
                                   org, several → they must name one, none
                                   → refused; admins unaffected)
PUT    /defaults                  admin/xbin:users. each present key
                                   replaces that setting wholesale (absent
                                   = untouched); default orgs must exist.
                                   → the resulting defaults

POST   /create                     owner/admin, a user creating a tile
                                   they will own, or an element granted
                                   target "xbin" at role writer (workspace
                                   management). body {path, runtime?,
                                   title?, expose?, owner?} → {path, files,
                                   owner?}. owner: "org:<id>" creates the
                                   tile OWNED by that org — gated by the
                                   org's Create knob / org admin (elements
                                   still need the xbin:writer capability).
                                   Non-admin humans (directly, or driving
                                   an element) get 403 when the path is
                                   reserved (tiles/…, root, shell, a ':'
                                   in any segment), inside a scope the new
                                   owner doesn't own, or still carries
                                   leftovers of a removed tile (grant
                                   rows, bindings, vault, others' access
                                   entries, a deployment record or
                                   checkpoint store; and, at the path or
                                   under it, a deployment's vault,
                                   registrations or data, a partitioned
                                   tile's partition mode record or its
                                   people's partition data — listed in the
                                   error; the path's owner is exempt);
                                   creation by anyone drops a deployment
                                   record left at the path, so the new
                                   tile starts with plain live reload; D82,
                                   docs/auth.md §Creating tiles. Default:
                                   the human creator becomes
                                   user-owner, admin/automation →
                                   workspace-owned. Under the org-only
                                   tile-creation policy (/defaults, D52) —
                                   or the account's noPersonalTiles (D88) — a
                                   non-admin's "user:" owner is refused and
                                   an empty one resolves to their single
                                   Create org. Same scaffolder as `bx
                                   new`; never overwrites. Clone/imports
                                   take the same owner? and assign the
                                   same default ownership. Every creator
                                   (admins too, every creation route) gets
                                   409 for a path nested with an existing
                                   component, or whose scope data key
                                   ("/" → "~") another scope has — e.g.
                                   apps~x beside apps/x, or workspace
                                   (D118, docs/resources.md). Every
                                   creator, admins included, every
                                   creation route (and `bx new`'s local
                                   write): 403 "can't create <path>: '+'
                                   isn't allowed in tile names (it names
                                   a tile deployment in URLs,
                                   /c/<tile>+<name>/) — pick another
                                   path" for a '+' in any segment (D127j).
                                   A directory already named with '+'
                                   keeps resolving (an exact match wins)
                                   but can't get deployments.
POST   /clone                      same authority as /create (the
                                   ownership path rule; the deputy clamp applies)
                                   + the human must have READ on `from`
                                   (copying is reading). body {from, to,
                                   owner?}
                                   → {path, from, rewritten, pendingGrants}.
                                   Forks a component: copies it (git history
                                   included), rewrites old-path references
                                   across its files, registers it fresh; a
                                   template instance's copy gets its
                                   manifest's merge driver for its own path
                                   (docs/overview/03-components.md §Templates).
                                   Secrets/resource data are NOT copied;
                                   unresolvable uses reject the clone.
GET    /builtins                   any. optional tile catalog
                                   [{name,title,description,defaultPath,installed}]
POST   /builtins/import            same authority as /create, checked on
                                   the resolved target (path? or the tile's
                                   defaultPath). body {name, path?, owner?}
                                   → {path, files, pendingGrants}
                                   — installs an
                                   embedded tile (docs/overview/14-lifecycle.md §Getting code in).
                                   A retired tile (`devbox`) → 410 {error}
                                   saying what replaces it.
GET    /builtins/updates            any. builtins (scaffold + imported tiles) with
                                   a newer embedded version. [{id,installPath,
                                   fromVersion,toVersion,adopted,files:[{path,
                                   status}],clean,conflicts}] (docs/overview/14-lifecycle.md §Keeping code fresh)
POST   /builtins/update             xbin:writer. body {id, mode:
                                   replace|merge|pr|pin|unpin}. replace
                                   overwrites, merge 3-way-merges (git merge-file);
                                   both → {files, notes?} and re-record provenance
                                   eagerly. No mode adds, removes or changes a
                                   tile's mode request, each xbin.json's
                                   top-level "partition" (any key case, as
                                   xbind reads it — docs/partitions.md):
                                   replace writes the INSTALLED value (present,
                                   absent or its list, spliced where the
                                   installed file has it; from the builder's
                                   own side of conflict markers an earlier
                                   merge left; for a file that doesn't parse,
                                   the mode xbind last read from the tile —
                                   and when even that is unknown the manifest
                                   is left as it is and the update stays
                                   offered); merge and "pr" undo upstream's own
                                   change to the key before git sees it, so
                                   the builder's line merges untouched. notes
                                   (absent when empty) name each manifest
                                   where upstream asks otherwise ("…/xbin.json:
                                   partition kept as installed (<ours>; upstream
                                   asks <theirs>): edit it deliberately to
                                   request a switch") — replace whenever it
                                   does, merge and "pr" when upstream changed
                                   the key — and a "pr" proposal's message
                                   says so too. mode "pr" (D49) writes NOTHING:
                                   the update is filed as a change proposal
                                   against the tile → {pr:{target,number,…}} —
                                   the tile's own plane reviews and `git am`s
                                   it, and provenance refreshes only when the
                                   PR closes merged. When upstream changed
                                   nothing but a partition there is nothing to
                                   propose: "pr" answers {files: [], notes}
                                   instead and records the version as applied
                                   (the tile's files are what a merge would
                                   leave). Idempotent per embed
                                   version; a newer embed auto-withdraws the
                                   stale open proposal. Such PRs carry
                                   kind:"builtin-update" + builtin/toVersion/
                                   toHash in their meta. Never touches
                                   template instances.
GET    /templates                   any. template blueprints (builtin ∪ workspace).
                                   [{id,source,title,description,defaultName,
                                   partition?,partitionSkipped?}] — partition:
                                   the mode new instances start in (the
                                   template block's "partition", e.g.
                                   ["user","global"]); partitionSkipped:
                                   "needs --isolate" when this xbind runs
                                   without isolation and won't write it. Both
                                   absent for a template that names no mode.
POST   /templates/new               same authority as /create on the
                                   resolved target; a workspace-template
                                   source also needs READ. body {source,
                                   path?, owner?, partition?} → {path,
                                   files, pendingGrants, partition?,
                                   partitionSkipped?} — instantiates a template
                                   into a named copy (docs/overview/03-components.md §Templates).
                                   The copy's top-level "partition" is the
                                   template block's (docs/partitions.md) unless
                                   the body says "partition": false or xbind
                                   runs without --isolate; the answer carries
                                   partition (the mode written) or
                                   partitionSkipped ("opted out" | "needs
                                   --isolate"), both absent when the template
                                   names no mode. Without the default the copy
                                   asks for no mode at all. An xbind older
                                   than partitioned tiles refuses the
                                   partition field (400 "need {source, path?,
                                   owner?}"); it never writes a mode, so send
                                   the field only when opting out. A
                                   builtin-template instance gets a read-only
                                   `template` git remote (below), and its repo
                                   is SEEDED from the template's repo (D50):
                                   main = the template snapshot + one commit of
                                   instantiate rewrites — so `git fetch
                                   template && git merge template/main` applies
                                   upstream template fixes cleanly (shared
                                   ancestry; the builder picks what to adopt).
                                   The instance's xbin.json is the template's
                                   JSONC without the block (and the comment
                                   lines above it), its partition the line
                                   after "{" — in a workspace whose template
                                   repo this xbind created, the served one
                                   plus that line. Its repo names the merge
                                   driver for it (merge.xbin-manifest, bx
                                   template merge-manifest: where the line
                                   merge of the template's change conflicts,
                                   xbin.json merges by keys;
                                   docs/overview/03-components.md §Templates).
GET    /templates/updates           authenticated. → {instances:[{path,
                                   template, head, legacy}]} — instances whose
                                   builtin template gained snapshots they
                                   haven't merged (filtered to tiles the
                                   caller can read; all local git plumbing).
                                   legacy = pre-seeding instance (unrelated
                                   history): its FIRST merge needs
                                   --allow-unrelated-histories, then it's
                                   normal.
GET    /templates/{repo}/{rest...}  authenticated. Read-only dumb-HTTP git server for a
                                   builtin template's source repo, e.g.
                                   /templates/agent.git/info/refs. Each instance
                                   has it as its `template` remote, so a builder
                                   pulls upstream fixes: git fetch template &&
                                   git merge template/main. Its xbin.json never
                                   changes the "template" block (instances never
                                   carry it): a repo xbind creates has none (nor
                                   the comment lines above it), one
                                   an older xbind created keeps its own; a
                                   change to the block is a snapshot whose
                                   message says so (an empty commit when nothing
                                   else changed; trailer Xbin-Template-Block).

GET    /code/tree                  admin OR code[:<component>]. ?component=<path> → {component, files:
                                   [{path,size}]} — a component's files.
GET    /code/file                  admin OR code[:<component>]. ?component=<path>&file=<rel> →
                                   {path, content|binary|truncated}.
GET    /git/log                    admin OR code[:<component>]. ?component=<path>&limit=N → {repo,
                                   commits:[{hash,short,author,date,subject,
                                   add,del,files}], remote}. From the component's
                                   OWN repo (each component is its own git repo);
                                   add/del/files = that commit's churn; remote =
                                   origin.
GET    /git/diff                   admin OR code[:<component>]. ?component=<path>&rev=<hash> → {repo,
                                   diff}. rev empty = uncommitted changes vs HEAD.
GET    /git/activity               admin OR code[:<component>]. ?component=<path> → {repo,
                                   remote, upstreamRef, local:[{t,a}],
                                   upstream:[{t,a}]|null}. Author-date (t, unix)
                                   + author (a) timeline of the component's
                                   history and, if a remote-tracking branch
                                   exists, its upstream — for activity charts.
GET    /git/remote-info            xbin:writer. ?url=<git-url> → {defaultBranch,
                                   tags:[…] (newest first), remote}. git ls-remote
                                   on a URL to preview versions before install.
POST   /git/import                 same authority as /create on the
                                   resolved path. body {url, path?, ref?,
                                   owner?} — clone a
                                   component in from a git remote (GitHub/GitLab/
                                   any git URL); path defaults to apps/<repo>, ref
                                   = a tag/branch. Its origin remote is kept (so
                                   it's updatable). → {path, remote, ref,
                                   pendingGrants}. Rejects local/file:// URLs and
                                   repos with no xbin.json/index.html.

Cross-tile change proposals ("code PRs", docs/bx.md §code pr). READ visibility
on the target is the whole gate (read = suggest, D48): admins; the target's
own principals; terminals/frames whose driving user can read the tile (D40);
elements holding code[:<target>]. xbind stores proposals under data/prs/ and
NEVER applies one — the target's own plane reviews and `git am`s it in its
terminal. A `pr` event (read-filtered like the mounts) fires on every
open/comment/state change.

POST   /code/prs                   read gate (above). body {target, title,
                                   message?, base?, series} — open a proposal.
                                   series = git format-patch mbox (≤4 MiB, must
                                   contain a diff), stored verbatim; base = the
                                   target rev it was formatted against (lifted
                                   from the base-commit trailer by bx). → the
                                   PR meta {number, target, from:{component,
                                   user,via} (verified, never self-reported),
                                   title, state:"open", created, …}.
GET    /code/prs                   ?target=<path>[&state=…] → {target, prs:[…]}
                                   (read gate); or ?from=1 → {prs:[…]} — the
                                   caller's own outgoing PRs across targets;
                                   admins with neither param get everything.
GET    /code/prs/summary           any authenticated → {counts: {path: n}} —
                                   open-PR counts, filtered to targets the
                                   caller can read (the shell's ⇄ badges).
GET    /code/pr                    read gate. ?target=<path>&n=<n> → full meta
                                   + events (the review thread).
GET    /code/pr/series             read gate. ?target=<path>&n=<n> → the raw
                                   mbox, text/plain, byte-exact (review first,
                                   then pipe into `git am --3way`).
POST   /code/pr/comment            read gate. {target, n, body} — append to
                                   the review thread (author ↔ maintainer).
POST   /code/pr/state              {target, n, state, comment?}. merged and
                                   rejected are the TARGET side's call (its
                                   own principals, write-level users, admins);
                                   withdrawn is the author's. Only open PRs
                                   close; no reopen — refile instead. 409 on
                                   double-close. A non-primary deployment's
                                   backend decides none: 403 "deciding a PR
                                   is the primary's act: a non-primary
                                   deployment's backend can't do it
                                   (<deployment>)".

GET    /grants                     admin — full table {grants, pending}.
                                   Any signed-in USER gets a filtered view
                                   (D26/D33): rows their orgs own
                                   (direction consumer), rows targeting
                                   their orgs' property (provider), and
                                   their own writable tiles' rows (mine —
                                   requesters are never blind). Shape:
                                   {grants: [{from,target,role,approvedBy?,
                                   approvedAt?,direction?}], pending:
                                   [{from,target,role,blocked?,approvable?,
                                   direction?,approvers?,warning?}], scope:
                                   "org"|"mine"} — blocked names the policy
                                   row that makes a request unapprovable;
                                   warning, on a partitioned tile's request
                                   on another partitioned tile's people's
                                   data, says whose data its code will reach
                                   ("…of every person who can read <x>", or
                                   "…who allows it" with partitionConsent
                                   on; docs/partitions.md);
                                   approvers hints who could (["org:<id>",
                                   "workspace-admin"], plus
                                   "transfer:org:<id>" when transferring a
                                   USER-owned requesting tile to that org
                                   would put it under its allowance, and
                                   "owner" when a personal tile's owner may
                                   approve it themselves, D88). Elements: admin only
POST   /grants                     admin — any. An org admin may approve on
                                   TWO edges (D26/D33): their org owns the
                                   REQUESTING tile and the target is
                                   intra-org or allowance-covered at the
                                   requested role; or their org owns the
                                   TARGET property (provider consent — no
                                   allowance needed). A PERSONAL tile's
                                   owner approves on it (D88): targets they
                                   own themselves or their personal
                                   allowance covers. Ceilings still apply;
                                   xbin/xbin:* and cap:sandboxes never
                                   delegable (D120). body
                                   {from,target,role} — approve/add; the
                                   stored row records approvedBy/approvedAt.
                                   → {ok, warning?}: warning is the pending
                                   row's approval warning (GET /grants) when
                                   a partitioned tile's grant reaches
                                   another partitioned tile's people's data.
                                   Approving a res:* / gpu:* grant restarts the
                                   caller's backend (that env/devices are captured
                                   at spawn) so it takes effect at once.
                                   Granting xbin or an xbin:* target to a tile
                                   that has non-primary deployments answers
                                   409 "<tile> has non-primary deployments:
                                   remove them before granting it <target>".
                                   Granting xbin, xbin:*, cap:sandboxes,
                                   cap:net-admin or cap:containers to a
                                   tile whose recorded partition mode, or
                                   whose code's request, has user
                                   partitions answers 409 "<tile> is
                                   partitioned (or asks to be): a
                                   partitioned tile can't hold <target> …"
DELETE /grants                     admin; also both D26/D33 edges (an org
                                   admin may always revoke their org's or
                                   their property's rows, a personal
                                   tile's owner that tile's — narrowing is
                                   safe). body {from,target,role}

GET    /bindings                   admin; signed-in users get a scoped view
                                   (D26/D33): their orgs' tiles + tiles
                                   they can write + rows whose refs point
                                   at THEIR orgs' provider tiles (the
                                   consumption of their property). Typed
                                   interface wiring (see
                                   docs/overview/11-interfaces.md; manifest fields in
                                   docs/elements.md).
                                   {bindings: {comp: {slot: provider|{ref,host,
                                    zone,listen}|[…]}},
                                    components: [{component, interfaces, provides,
                                                  partitioned?}],
                                    pending: [{component, slot, kind, service,
                                              expose?, default?, approvable,
                                              options: [{id, label, blocked?}]}],
                                    exposes: [{component, slot, kind, paths?,
                                              proto?, port?, approvable,
                                              routes: [{source, host?, zone?,
                                                        listen?}],
                                              options: [{id, label}]}],
                                    inert: {comp: {slot: reason}},
                                    approvable: {comp: true},
                                    netOptions: {comp: [{id, label, blocked?}]},
                                    sandboxNetOptions: {comp: [{id, label,
                                                                blocked?}]}}.
                                   `exposes` is every exposed endpoint in
                                   view, bound or not, with ALL its routes —
                                   an endpoint takes many (D79) — and the
                                   sources it can bind: the "add a hostname /
                                   port" data (pending lists a slot only while
                                   it has no route).
                                   `pending` is the unbound slots + candidate
                                   providers — the bind-on-install prompt;
                                   expose:true rows are unpublished exposed
                                   endpoints (bind = publish, docs/ingress.md).
                                   `approvable` (the map, and the flag on each
                                   pending row) names the components whose
                                   wiring THIS caller may change: every one
                                   for a workspace admin, the tiles of orgs
                                   they administer for an org admin (D26),
                                   the tiles you own personally (D88 —
                                   within your allowance); a tile you
                                   merely write is listed but not
                                   approvable — UIs show its wiring
                                   read-only. An option `blocked:true` is one
                                   POST /bindings refuses (a net ref outside
                                   the owning org's network sets — for
                                   everyone; outside a personal tile
                                   owner's allowance — for them, labelled
                                   "outside your network allowance"; an
                                   http provider that is partitioned without
                                   a global instance, on an unpartitioned
                                   component's slot — for everyone, 409,
                                   labelled "partitioned, no global
                                   instance"); pickers grey it out.
                                   `partitioned` (components rows) is true
                                   for a tile whose recorded mode has user
                                   partitions: its bindings are global binds
                                   (docs/partitions.md §Bind types).
                                   default:"org" marks an unbound net slot on
                                   an org-owned tile with network sets — it
                                   is already satisfied (D54); binding only
                                   narrows or overrides. default:"personal"
                                   is the same on a personal tile whose
                                   owner has network sets (D88). `inert` lists net
                                   bindings that are stored but resolve to
                                   no egress (a set narrowed, a transfer),
                                   with the reason. Net options carry `org`
                                   (org-owned tiles; label = the live reach),
                                   `personal` (personal tiles; label = the
                                   owner's personal network, D88),
                                   `none` and one `set:<name>` row per
                                   network set (D65) — blocked for anyone
                                   but a workspace admin, for a provider-
                                   only set, and where the org's sets don't
                                   cover it; options the org's sets refuse
                                   say "not covered". `netOptions` maps
                                   every visible net slot's component to
                                   its full option list, bound or not (the
                                   re-bind pickers read it).
                                   `sandboxNetOptions` does the same for
                                   components with sandbox-net slots — a
                                   sandbox manager's network classes
                                   (docs/isolation.md §Network egress):
                                   one list per component (every class of
                                   a tile offers the same), never host or
                                   a provider tile; a set that says host
                                   is blocked. Their pending rows carry no
                                   default (an unbound class is none), and
                                   a class whose binding resolves to no
                                   network is listed in `inert` by slot
POST   /bindings                   admin; an org admin within D26 (their
                                   org owns the component; targets
                                   intra-org or allowance-covered — the
                                   iface:svc@tile#instance grammar pins
                                   provider/instance), D33 (every ref is a
                                   provider THEIR org owns — provider
                                   consent), or D41 (expose host/zone
                                   routed through a terminator tile their
                                   org owns; listen/runtime never); a
                                   personal tile's owner within D88 (unbind,
                                   none/personal, their own provider tiles,
                                   or allowance-covered targets). body {component, slot, provider} or
                                   {component, slot, providers:[…]} (the full
                                   set for a multi:true http slot). Refs are
                                   provider[#instance]; an instances-provide
                                   binds to a specific instance only. — bind
                                   a requested interface to a provider (a builtin
                                   id like internet/host/lan:<cidr>/
                                   internet:<spec>, `org` — the owning org's
                                   network sets, live; org-owned tiles only —
                                   `personal` — the owner's personal network
                                   sets, live; user-owned tiles only (D88) —
                                   `none` — explicitly no egress — or
                                   `set:<name>` — one named network set, a
                                   WORKSPACE-ADMIN act: 403 for org admins,
                                   provider-only sets refused, inside the
                                   org's sets on org-owned tiles (D65); or a
                                   tile path). On an org-owned tile with
                                   network sets every net ref must be inside
                                   the sets (400 names the set; ws-admins
                                   widen the set instead) — org admins bind
                                   inside them without an allowance (D54).
                                   Owner-only (agents can't self-bind).
                                   Restarts the component (+ a net provider whose
                                   roster changed) so wiring takes effect at once.
                                   A sandbox-net slot (a sandbox manager's
                                   network class) takes one of none,
                                   internet, internet:<spec>, lan:<cidr>,
                                   org, personal, set:<name> — host, a
                                   provider tile, a set that says host and
                                   #instance are 400 — under the same
                                   approval rules and ceilings as net; its
                                   (un)binding restarts nothing (the running
                                   sandboxes of that class re-resolve it).
                                   For an EXPOSED endpoint slot (docs/ingress.md)
                                   the body also carries the route config:
                                   {host} or {zone} (http; source "runtime" or a
                                   terminator tile), {listen} (stream; source
                                   "runtime"); binding = publishing. An exposed
                                   endpoint takes MANY routes (D79): add:true
                                   appends this one (another hostname, zone or
                                   host port — each still exclusive across the
                                   workspace, none twice on the slot) instead
                                   of replacing the slot's routes; an org
                                   admin's rights are judged on the route
                                   added. A stream INTERFACE slot binds
                                   "provider#expose-slot".
                                   A new ref of an unpartitioned component's
                                   http slot to a tile whose partition mode
                                   has user partitions and no global
                                   instance is 409: no call of it would
                                   reach that tile (docs/partitions.md); a
                                   ref the slot already holds isn't judged
                                   again. A partitioned
                                   component binds under the same rules as
                                   any other: its bindings are global binds,
                                   seen by every person's partition; binding
                                   its http slot to another partitioned tile
                                   answers {ok, warning} — the approval
                                   warning of POST /grants. Into a tile
                                   that runs reviewed code only (POST
                                   /partitions/reviewed), a provider that
                                   isn't partitioned and whose primary
                                   isn't protected is 409.
DELETE /bindings                   admin / owning-org admin (always) /
                                   provider-org admin (withdrawing
                                   service). body {component, slot} — clear a binding
                                   (for an exposed slot: unpublish — the host
                                   404s, a stream port closes + live flows end).
                                   With provider and/or host|zone|listen:
                                   remove only the matching route(s) — 404 if
                                   none; the provider-side right is judged on
                                   what is removed
PUT    /iface-instances            self or admin. body {component?, instances:
                                   {"<id>": "/provider-relative/prefix"}} — a
                                   provider with provides {instances:true}
                                   registers its runtime instances (replaces
                                   the map; bound requesters re-wire). Bind as
                                   provider#id. Paths are PROVIDER-RELATIVE
                                   ("/m/1"): xbind injects /api/<provider><path>
                                   into consumers. Workspace-absolute paths
                                   ("/api/<self>/…") are rejected 400 — they
                                   would double the prefix, and they bake the
                                   install path into persisted state (stale
                                   after a rename/clone). Trailing "/" is
                                   normalized away.
                                   From a deployment that isn't the primary
                                   (main beside another primary included):
                                   200, today's body plus dormant:true; the
                                   map is stored for that deployment
                                   (§Filesystem contract) and never routed —
                                   no grants event, no re-wiring, not even
                                   with its deliveries on. provider#id always
                                   resolves against the provider primary's
                                   map. From a person's partition of a
                                   partitioned tile the same: 200 with
                                   dormant:true, stored for that partition
                                   and never routed (instances are the
                                   global instance's, docs/partitions.md)

PUT    /ingress-hosts              self or admin. body {component?, hosts:[…]}
                                   — a tile with a DELEGATED-ZONE http expose
                                   binding registers the concrete hostnames it
                                   serves (docs/ingress.md; replaces the set,
                                   [] clears). Every host must sit inside one
                                   of the tile's own bound zones (403 outside —
                                   the authority boundary), be a valid bare
                                   hostname, and not collide with any exact-
                                   bound host or another tile's registration
                                   (409). Routes update live; no restart.
                                   Only the primary's hosts route, and the
                                   conflict check meets only other tiles'
                                   primaries' hosts. From a deployment that
                                   isn't the primary (main beside another
                                   primary included): 200, today's body plus
                                   dormant:true; the set is stored for that
                                   deployment, zone-validated but not
                                   conflict-checked, and never routed — no
                                   reconcile, not even with its deliveries on.
                                   A person's partition of a partitioned tile
                                   likewise: 200 dormant:true, stored for it,
                                   never routed
GET    /ingress-routes             terminator tiles + admin. {routes: [{host,
                                   component, slot, paths, source, zone?}]} —
                                   the concrete host→tile routes. A tile with
                                   provides {kind:"ingress"} sees the routes
                                   bound THROUGH IT (its proxy/ACME config
                                   input); admins see all; others 403. A
                                   non-primary deployment of a terminator,
                                   and a person's partition of one, reads
                                   {"routes":[]}.
GET    /ingress                    admin. The whole ingress picture: {exposes:
                                   [{component, slot, kind, paths|proto+port,
                                   source, host|zone|listen, routes:[{source,
                                   host?, zone?, listen?}], blocked?}] (routes =
                                   every route of the slot, D79; the scalars
                                   repeat the first),
                                   routes, ingressHosts, terminators, streams:
                                   [{…, error?, active}], forwards, httpListener:
                                   {listen, tls}} — every exposes slot with its
                                   binding + policy state, live routes, stream
                                   listener health, and the builtin listener.

POST   /lifecycle                  admin, the tile's user-owner, or an
                                   owning-org admin (D24: lifecycle is the
                                   owner's). body {component, state} — component
                                   lifecycle (docs/overview/14-lifecycle.md). state
                                   also takes `hidden` — disabled + kept
                                   out of sidebars/listings until unhidden
                                   (D42; refused while offloaded). state:
                                   enabled | disabled | offloaded | offloaded-full.
                                   A non-enabled backend is not spawned (the proxy
                                   returns 409 + an X-XBin-Lifecycle header);
                                   disabling stops a running backend now. Offload
                                   archives then frees local bytes (data, or +
                                   source/term-env for -full, which ends the
                                   tile's terminal sessions first — 502, nothing
                                   removed, if one won't end); enabling an
                                   offloaded component restores it. Offloading
                                   a partitioned tile is refused (409 "can't
                                   offload …", nothing archived or stopped;
                                   docs/partitions.md). State is in the
                                   overview's component list (state field).
                                   Lifecycle is the tile's: disabling, hiding
                                   or offloading stops every deployment;
                                   enabling starts the primary (an alwaysOn
                                   primary at once), a deployment beyond it
                                   by its own alwaysOn switch or on its next
                                   request. A change emits the bare reload as
                                   before and, for each deployment that isn't
                                   the primary, a deployments event {op:
                                   "reload", deployment}. A qualified name
                                   (<tile>+<name>) has no lifecycle (404).
                                   Offloading archives every deployment's
                                   data before removing anything; beyond main
                                   it frees the kv data, and file resources
                                   stay on disk.
                                   Every state but enabled also stops the tile
                                   sandboxes the tile manages (state kept), and
                                   an offload is refused 409 — nothing archived
                                   — while any of them holds state (an upper, a
                                   disk, a snapshot): offload can't carry it yet.
                                   {ok, state}; enabling an offloaded tile adds
                                   what its restore answers (POST /restore):
                                   deployments, sandboxesSkipped?, and
                                   dataErased? / dataMissing? when a sealed
                                   backup's data couldn't come back.

POST   /partitions/mode            a tile manager (the tile's user-owner, an
                                   admin of its owning org, or a workspace
                                   admin) acting as a person: their own
                                   session, app, device or the root token, or
                                   the admin tile's frame under their login;
                                   every other tile principal (instance,
                                   frame, terminal, agent session) 403.
                                   body {tile, act: "keep"|"switch", from, to,
                                   confirm?, yes?, dryRun?} — decide a
                                   partition mode switch request R → Q
                                   (docs/partitions.md §The mode).
                                   from/to are {user, global} (null:
                                   unpartitioned) and must still be the
                                   request's R and Q, else 409 with the
                                   current partition {state, from, to?,
                                   declined?}; 409 too for an invalid request
                                   or an unreadable mode record. keep answers
                                   an open request: R runs again at once,
                                   nothing is deleted — {ok, tile, act, mode,
                                   declined}. switch answers an open or a
                                   declined request; confirm must be the tile
                                   path (400); user partitions need xbind's
                                   --isolate (409); an offloaded tile is 409;
                                   when to has user partitions and the tile
                                   binds sandbox managers whose GET
                                   /sbx/hello caps lack "partitions", 409
                                   {managers} unless yes. It stops every
                                   instance, deletes the tile's data — from
                                   or to unpartitioned: every namespace
                                   (main's and every deployment's), vault file
                                   and registration (cron, bus, interface
                                   instances, ingress hosts; people's
                                   personal binds on the tile, counted with
                                   the registrations), and erases its
                                   ns: backup keys (tile: stays); removing
                                   "global": global's namespace, vault and
                                   registrations and global's ns: key only;
                                   adding "global": nothing — then records R
                                   := Q and tells each person whose partition
                                   went (push kind tile.partition-deleted).
                                   → {ok, tile, act, from, to, deletes,
                                   wiped: {namespaces, partitions, vaultKeys,
                                   registrations, bytes, subkeys,
                                   keyFilesLeft?}, keeps: [text], people?,
                                   managers?, archiver?, eraseError?}
                                   (eraseError, keyFilesLeft: keys erased
                                   whose files aren't removed yet — refused
                                   everywhere all the same). A wipe that
                                   fails part-way, or an erase of the backup
                                   keys that fails, is 500 with wiped:
                                   nothing is recorded, the request stays
                                   open, a retry finishes it. A tile at the
                                   path "workspace" never counts or deletes
                                   the workspace-level resources. dryRun
                                   counts the same, deletes nothing and needs
                                   no confirm. Audited. A request opening
                                   pushes to the tile's managers (kind
                                   tile.partition-switch; at most one per
                                   tile every 15 minutes), and every mode
                                   change reloads the tile's frames (event
                                   reload).
GET    /partitions/binds           a person (their own personal binds), or
                                   admin — an admin person, the root token or
                                   a tile holding xbin:admin (the admin
                                   console) — every living person's; every
                                   other tile principal 403.
                                   → {binds: [{id, user, requester, slot,
                                   provider, at, live, why?}]} — live: the
                                   bind holds now; why: why not (the provider
                                   changed owner, the requester no longer
                                   partitions, …). A deleted person's rows,
                                   or an earlier incarnation's under the same
                                   id, are never listed (docs/partitions.md
                                   §Bind types).
POST   /partitions/binds           a person's own act: their session, app or
                                   device (never a tile principal, view-as or
                                   the root token — 403), and never an
                                   admin's (403: an admin's bind is always a
                                   global bind, POST /bindings). body
                                   {requester, slot, provider} — wire
                                   provider into the caller's OWN partition
                                   of requester. 403 unless the caller owns
                                   provider personally (user:<id>) and can
                                   read requester, or when the policy
                                   ceiling denies the edge; 404 an unknown
                                   tile, answered only past those checks (a
                                   tile you own, a path you may read); 409
                                   when requester isn't partitioned or is
                                   paused, slot isn't a multi:true http slot,
                                   provider is partitioned, doesn't provide
                                   the slot's service or exposes instances,
                                   or is already bound on the slot for
                                   everyone. Only that person's partition
                                   instance gets the row in
                                   XBIN_IFACE_<SLOT> (restarted: its env is
                                   captured at spawn) and only their frames'
                                   xbin-interfaces meta lists it, each with
                                   personal: true; it lets a call through
                                   only from requester acting in their
                                   partition, while they still own provider
                                   (every other caller: today's 403). Adding
                                   the same bind again answers the existing
                                   one. → {ok, bind: {id, user, requester,
                                   slot, provider, at, live}}; publishes
                                   grants for requester to that person's
                                   sockets and frames only.
DELETE /partitions/binds           the bind's person, or admin (as for GET).
                                   body {id} or {requester, slot, provider[,
                                   user]} (user: whose, default the caller's
                                   own; an admin's {id} matches anyone's) →
                                   {ok, removed: [{id, user, requester, slot,
                                   provider, at}]}; 404 when nothing matches
                                   — a deleted person's rows, or an earlier
                                   incarnation's, never do. Restarts that
                                   person's partition instance of requester.
                                   A personal bind also goes when its
                                   provider changes owner, its person is
                                   deleted, requester switches between user
                                   partitions and unpartitioned, or a tile is
                                   created at its requester's or provider's
                                   path (a removed tile's binds never reach
                                   the new one).

GET    /partitions/consents        a person's own session, app or device
                                   (PersonOnly: tile code — frames,
                                   instances, terminals, agent sessions —,
                                   view-as and the root token 403). →
                                   {policy: {partitionConsent}, consents:
                                   [{from, to, at, via}], asked: [{from, to,
                                   at}]}: the person's consents to
                                   cross-tile partition edges (kept while
                                   the policy is off; they apply again when
                                   it returns) and the edges they were asked
                                   about in the last day and haven't allowed
                                   (docs/partitions.md §Calls between
                                   partitioned tiles)
POST   /partitions/consents        PersonOnly, as above. {from, to}: let
                                   partitioned tile from use the person's
                                   data in partitioned tile to. Only while
                                   the workspace policy partitionConsent is
                                   on (else 409); both tiles partitioned
                                   (409), existing (404) and readable by the
                                   person (403); a path holding "→" 400.
                                   Kept in
                                   data/partitions/consents/<uid>.json (a
                                   person recreated under the same id
                                   inherits none; a tile deleted, moved or
                                   switching mode takes every consent
                                   naming it). A record this xbind can't
                                   read is kept as it is: 409 (/alerts kind
                                   partition-consents). → the GET view;
                                   audited; publishes `partitions` op
                                   consent
DELETE /partitions/consents        PersonOnly, as above; either setting.
                                   {from, to} (body, or ?from=&to=): take a
                                   consent back. With the policy on, the
                                   next call and data reach are refused, and
                                   from's backend instance of the person
                                   stops (it starts again on the next
                                   request; a proxied stream a page,
                                   terminal or agent session of from opened
                                   before lasts until it closes). → the GET
                                   view plus revoked: whether there was a
                                   consent to take back; audited; 409 as
                                   for POST
GET    /partitions/ledger          PersonOnly, as above. ?tile= ?days=1-90
                                   (30). The egress ledger (counts per day,
                                   never contents): kind edge — an allowed
                                   call or data reach into the same person's
                                   partition of another partitioned tile, a
                                   bus subscription there once when made
                                   and once per delivery —,
                                   provider — a call to a tile that isn't
                                   partitioned (a global or a personal
                                   bind), or to a partitioned tile's
                                   deployment beyond its primary, target
                                   <tile>+<name> —, bus and trigger (a
                                   shared resource's reach isn't counted).
                                   → {days, rows:
                                   [{tile, day, kind, target, count}] (the
                                   person's own), totals?: [{kind, target,
                                   count, people}] (with tile: the tile's
                                   writers, managers and admins; for all but
                                   admins every personal tile's target is
                                   "(a personal tile)"), people?:
                                   [{user, tile, kind, target, count}]
                                   (admins)}. Kept 90 days in
                                   data/partitions/<tile-key>/<dep>/<pkey>/
                                   ledger.json; both consent settings
GET    /partitions/edges           admin. ?days=1-90 (30). → {days, policy:
                                   {partitionConsent}, edges: [{from, to,
                                   granted, people, calls, consented}]}: the
                                   edges from a partitioned tile into
                                   another's people's data — granted now, or
                                   counted in people's ledgers in the window
                                   — with how many people used and allowed
                                   each: what turning partitionConsent on
                                   starts asking about (the admin tile's
                                   Policies tab shows it first)
GET    /partitions                 anyone; what it answers depends on who
                                   asks (docs/partitions.md §Operating
                                   people's partitions). → {features:
                                   ["partitions/1", "mode-switch/1",
                                   "consents/1", "personal-binds/1",
                                   "global-address/1", "partition-ops/1",
                                   "log-share/1", "credential-confirm/1",
                                   "partition-mail/1", …] (what this xbind
                                   serves; a 404 is an
                                   xbind without partitions), policies?:
                                   {partitionConsent,
                                   credentialResetConfirm} (people and
                                   admins, never tile code), …}.
                                   ?tile=<t> (one the caller can read, else
                                   404): tile, state (partitioned |
                                   unpartitioned | pending | invalid), spec
                                   {user, global}, request {spec, since,
                                   declined} | null, error?, limits
                                   {maxRunning, partitionBytes},
                                   reviewedOnly {on, by?, at?,
                                   unprotected?} (POST
                                   /partitions/reviewed); partitions: the
                                   caller's own row {user, partition,
                                   partitionId, state (active | dormant |
                                   orphaned), why?, running, instance?:
                                   {tile, deployment, partition, state,
                                   gen, uptimeSec, rssKb, restarts,
                                   error? (their own row only),
                                   errorClass?}, lastStarted?,
                                   created, lastExit?, restarts?,
                                   crashLoop?, bytes, registrations:
                                   {cronJobs, busSubscriptions,
                                   ifaceInstances, ingressHosts,
                                   missedTicks, dormantDrops, vaultKeys},
                                   logShare?: {until}, ledger?: [{kind,
                                   target, count}] (30 days), mail?:
                                   {pending, bytes, expired,
                                   undeliverable?} (their inbox's counts,
                                   once it has held an item)} — for admins
                                   every person's metadata row (never
                                   content, vault key names, log lines or
                                   mail: its counts only; logShare, no ledger;
                                   instance.errorClass, never the error's
                                   text), orphaned ones included; bytes are
                                   measured at most once a minute; totals
                                   {people, running, bytes, cron, bus} (the
                                   tile's writers, managers, admins); trust
                                   {writers, admins, liveReload, protected,
                                   lastCodeChange? {at, by, how, result}
                                   (the primary's last code move, for a
                                   tile with deployments), reviewedOnly,
                                   providers: [{tile, writers, liveReload,
                                   protected, lastCodeChange?}], warnings}
                                   (its people);
                                   consents (the person's, policy on);
                                   binds (personal binds whose requester
                                   is the tile: the person's own, every
                                   live one for admins); orphans (admins);
                                   notices (the person's). A tile's own
                                   credentials (its frames, backend,
                                   terminals) get the tile-level fields and
                                   features only. Without tile: tiles:
                                   [{tile, state, spec, request, error?,
                                   mine?: {partition, state, running,
                                   bytes}, totals? (admins), trust?
                                   (admins: the warnings), globalBinds?,
                                   reviewedOnly?, capsHit? {at, kind
                                   (evicted | refused | deferred), count}
                                   (the last 24 hours), and with
                                   ?untracked=1 untracked? (files the
                                   tile's own repository doesn't track, at
                                   most 20), untrackedCount?,
                                   untrackedError? (admins)}] — the tiles
                                   that are or ask
                                   to be partitioned, that the caller can
                                   read — and for admins isolated, orphans
                                   [{tile, deployment, partition, user,
                                   reason, since}] and now; for a person
                                   credentials [{id, kind (invite |
                                   password | email | sso-provider), by,
                                   at, until, email?, issuer?}]
                                   (credentials an admin made for
                                   them, waiting for their answer) and
                                   notices [{id, at, kind, tile?, text,
                                   hold?}]. Cache-Control: no-store
POST   /partitions/stop            a person's act: their own session, app or
                                   device, the root token, or the admin
                                   tile's frame driven by one (anything
                                   else 403). {tile, partition: "user:<id>"}:
                                   stop that partition's instance — the
                                   person their own, a tile manager or an
                                   admin anyone's. Its token is revoked
                                   first; its data stays and the next
                                   request starts it again. In order: a
                                   partition that isn't user:<id> 400; a
                                   tile the caller can't read 404 as a
                                   missing one (unless they name their own
                                   partition of it); someone else's
                                   partition 403, before anything of it is
                                   said; a tile that isn't partitioned 409;
                                   a person without a partition of the
                                   tile 404. → {ok, tile, partition};
                                   audited
POST   /partitions/reset           as for stop: the person their own, an
                                   admin anyone's. {tile, partition,
                                   confirm: "<tile> <partition>"} (else
                                   409 {error, confirm}). Stops the
                                   instance, ends the person's terminal and
                                   agent sessions on the tile (a new one
                                   answers 409 until the reset is done),
                                   deletes — in every deployment it has
                                   data in — the partition's namespaces
                                   (only when the tile roots its scope: a
                                   partitioned tile in another's scope
                                   uses none), vault, registrations,
                                   records, ledger, log share, backend log,
                                   terminal layers and agent-session
                                   history, and erases the tile's own
                                   backup keys of it
                                   (part:<tile>/<deployment>/<partition
                                   id>; its archives become unreadable —
                                   never the scope root's). The refusals
                                   in stop's order. 409 while the tile is
                                   paused. → {ok, tile, partition, deleted:
                                   {namespaces, layers, histories, subkeys,
                                   bytes}}; audited; an admin's reset tells
                                   the person (push tile.partition-reset
                                   and a notice)
POST   /partitions/purge           admin (as for stop). {tile?,
                                   partition?} (partition: the partition id
                                   u-<32 hex>, or user:<id> of the person it
                                   was): delete orphaned partitions — their
                                   person deleted (or the id someone else's
                                   now), their tile removed — now instead
                                   of 30 days after, as reset deletes one,
                                   subkey erased. A live person's
                                   partition 409 (reset it), none 404; a
                                   paused tile's orphans are skipped with
                                   an error. A removed tile left with
                                   nothing loses its mode record. → {ok,
                                   purged: [{tile, deployment, partition,
                                   user, reason, since, deleted, error?}]};
                                   audited
POST   /partitions/share-log       PersonOnly (as for /partitions/consents).
                                   {tile, days?: 1-14 (7)}: share the
                                   person's partition's backend log of the
                                   tile with its managers and admins until
                                   then (GET /logs?user=). Kept in the
                                   partition's directory (a reset, a purge
                                   or a switch takes it). 404 without a
                                   partition of the tile. → {ok, tile,
                                   shared, until}; audited
DELETE /partitions/share-log       PersonOnly. {tile}: end the share now. →
                                   {ok, tile, shared: false}
POST   /partitions/credential-confirm
                                   PersonOnly. {id, allow: bool}: the
                                   person allows (it takes effect) or
                                   refuses (a link stops working; a
                                   password or email is dropped; a
                                   provider change unbinds their SSO
                                   email) a credential an admin made for
                                   them while the workspace policy
                                   credentialResetConfirm was on (GET
                                   /partitions' credentials). Unanswered,
                                   it takes effect 24 hours after they were
                                   told; a refusal still revokes one that
                                   is unused. 409 {error, decision:
                                   "already-effective"}: a link no longer
                                   pending (redeemed, replaced or expired)
                                   — change the password and sign out
                                   everywhere. 404 for one that isn't
                                   waiting. → {ok, id, kind, decision:
                                   allowed|refused}; audited; a notice
POST   /partitions/reviewed        admin (as for stop). {tile, on: bool}:
                                   the tile's "reviewed code only" switch
                                   (docs/partitions.md §Reviewed code
                                   only). on needs the tile partitioned
                                   (409) and the primary of the tile and of
                                   every non-partitioned provider bound to
                                   it protected (409 {error,
                                   unprotected}); while on, unprotecting
                                   any of them (POST /deployments/protect
                                   {on: false}) and binding into the tile
                                   a provider that isn't partitioned and
                                   whose primary isn't protected answer
                                   409. off always succeeds. → {ok, tile,
                                   reviewedOnly: {on, by?, at?,
                                   unprotected?}}; audited

POST   /partitions/mail            a partitioned tile's global instance (its
                                   instance token, on the primary) or a
                                   person's partition of it (its backend,
                                   that person's frames, terminals and agent
                                   sessions); everyone else 403 — people
                                   outside the tile's credentials (admins
                                   included), the root token, frames and
                                   terminals acting in no person's
                                   partition (they read global's inbox,
                                   below, but send nothing), view-as, other
                                   tiles, a non-primary deployment and every
                                   delivery principal. {to, topic, data?, ttl?,
                                   source?} → {ok, id}. The global instance
                                   mails "user:<id>" — a person who exists,
                                   is enabled and can read the tile, else
                                   404 "no such person here: user:<id>",
                                   whatever the reason — or "global"; a
                                   person's partition mails "global" only
                                   (403 otherwise, and on a tile without a
                                   global instance). from is stamped by
                                   xbind — global or user:<id> — never read
                                   from the body (a from field is ignored).
                                   Limits: topic ≤ 256 bytes, topic + data
                                   ≤ 1 MiB (413); an inbox holds ≤ 1000
                                   items and ≤ 64 MiB (507 to the sender);
                                   in the global instance's inbox each
                                   sender (global, each person) has ≤ 100
                                   items and ≤ 8 MiB waiting (507 "your
                                   share … is full" to that sender only).
                                   ttl: seconds, 1–2592000 (default 7 days;
                                   400 outside). source (the global instance
                                   to a person only): a private trigger's
                                   source, counted as kind trigger in that
                                   person's egress ledger — never the
                                   content. 409 while the tile is paused (a
                                   mode switch pending or deleting); 503
                                   while the vault is sealed. Stored in
                                   data/partitions/<tile-key>/<dep>/mail.db,
                                   sealed with the vault barrier; not backed
                                   up; not in the audit stream (data plane).
                                   Rings the addressee's doorbell (below)
GET    /partitions/mail            the callers of POST (403 and 409
                                   alike), and — the global instance's
                                   inbox only — the tile's frames, terminals
                                   and agent sessions acting as global (the
                                   owner token's frames, root terminals; on
                                   the primary; never view-as), reading the
                                   caller's OWN inbox only — its person's
                                   partition's, or the global instance's; no
                                   parameter names another, and admins see
                                   counts only (GET /partitions).
                                   ?after=<id> ?limit=1-1000
                                   (100) → {items: [{id, from, topic, data,
                                   at, expires}], more}: oldest first. A
                                   page stops at limit or past ~8 MiB of
                                   data (at least one item): only more
                                   false ends the inbox. Expired items are
                                   dropped (counted as expired); an item
                                   that can't be opened (sealed under
                                   another key, damaged) is dropped
                                   (counted as undeliverable) and never
                                   stops the page. 503 while the vault is
                                   sealed and the page would hold items (an
                                   empty inbox answers 200)
POST   /partitions/mail/ack        the same callers as GET. {ids} (≤ 1000)
                                   → {ok}: removes those items of the
                                   caller's own inbox; ids acked or expired
                                   already are nothing to do. It never
                                   opens an item: it works while the vault
                                   is sealed. Delivery is at-least-once
                                   until acked or expired: dedupe by id

POST   /backup                     admin. body {component} — build a self-
                                   describing tar (source + scope data + terminal
                                   env + a manager tile's sandbox definitions,
                                   never their state; NOT vault/env-layer) and
                                   stream it to the component's bound @archive
                                   provider. {ok, version}
                                   A tile with deployments adds deployments/
                                   (its record and checkpoint store) right
                                   after backup.json, and a deployments
                                   section {record, checkpoints} in the
                                   manifest (schema still 1); a tile without
                                   one gets today's archive. The archive
                                   also carries the registration files of
                                   its deployments beyond main. When the
                                   primary isn't main, every backup first
                                   archives the primary's data in a
                                   deployment archive of its own (key
                                   .deployments.<tile-key>.<name>, manifest
                                   schema 2, naming its deployment), which
                                   the tile's archive lists; other
                                   deployments' data is archived only on
                                   request (POST /deployments/backup, or a
                                   deployment's schedule), under its own key,
                                   so it never uses the tile's retention.
                                   Readers take schema 1 and 2; an older
                                   xbind refuses a deployment archive.
                                   Sealed backups (docs/overview/14-
                                   lifecycle.md §Sealed archives): with a
                                   vault barrier every archive is sealed —
                                   XBINSEAL, a cleartext header naming the
                                   backup subkey's id, then AES-256-GCM in
                                   64 KiB chunks over the same tar — under
                                   its subject's key (the main archive:
                                   tile:, the data: ns:<namespace>), and the
                                   PUT carries X-XBin-Backup-Subkey: bk-….
                                   A scope root's main data goes to a data
                                   archive of its own (key .data.<tile-key>,
                                   schema 3, kind "data"), written before
                                   the main archive, whose manifest (schema
                                   3) names it: data {key, version, subkey}.
                                   Both manifests carry the same random
                                   backupId, and a restore pairs them only
                                   when it and the key match. A scope that
                                   declares no resource gets no data
                                   archive. The data PUT must answer a
                                   version of [A-Za-z0-9][A-Za-z0-9._:-]
                                   (≤ 128), or the backup fails before the
                                   main archive is written. The plaintext-
                                   vault mode (--insecure-vault, --no-auth)
                                   writes today's plain tars. 502 while the
                                   vault is sealed, and while it isn't set
                                   up yet (no barrier, not the plaintext-
                                   vault mode): no archive is written.
                                   Readers take schema 1-3 and plaintext
                                   archives of any age; an older xbind
                                   refuses a sealed archive.
                                   A partitioned tile's people's partitions
                                   (docs/partitions.md §Backups) are
                                   archived after the main archive, each
                                   under a key of its own
                                   (.partitions.<tile-key>.<dep>.<partition
                                   id>, schema 3, kind "partition"), sealed
                                   under that partition's own backup key
                                   (part:<tile-key>/<dep>/<partition id>);
                                   the main, data and deployment archives
                                   hold none of it (the main manifest lists
                                   the global instance's cron jobs and bus
                                   subscriptions only). A partition that
                                   can't be archived doesn't fail the
                                   backup; in the plaintext-vault mode none
                                   is archived. The answer then gains
                                   partitions: {archived, failed?:
                                   ["user:<id> (<partition id>): <why>"],
                                   skipped?: "<why none>"}; a tile without
                                   people's partitions answers as before
GET    /backups?component=…         admin. the archiver's version list passed
                                   through: {versions:[{version,time,size}]}
POST   /restore                    admin. body {component, version?, file?,
                                   confirm?}. An archive made before the
                                   tile's last partition mode switch that
                                   deleted data (docs/partitions.md
                                   §Backups) restores only with confirm set
                                   to that switch's date (YYYY-MM-DD) —
                                   into the global instance's namespace, at
                                   today's keys, never a person's partition
                                   — else 409 {error: "this backup (made …)
                                   is older than <tile>'s partition mode
                                   switch on <date> (<from> → <to>):
                                   restoring it brings back data the switch
                                   deleted …", switch: {at, from, to,
                                   confirm}}, before anything stops; the
                                   deployment archives it lists go with it.
                                   The switch is remembered apart from the
                                   tile's mode history (whose trimming never
                                   drops it). While the tile's mode record
                                   can't be read, every archive asks, with
                                   its own creation date (switch: {unknown:
                                   true, error, confirm}).
                                   A person's partition archive is never a
                                   tile (409; POST /partitions/restore), and
                                   no single file of one is ever served.
                                   No file → restore the whole version (stops the
                                   component, replaces its data/source from the
                                   archive; version defaults to latest). With file
                                   → stream one member back (recover without a full
                                   rollback): xbind fetches the version and
                                   extracts it itself (data/… of a split
                                   archive from its data archive); 404 for
                                   a member it doesn't hold, 409 when a key
                                   is erased or unknown. Restore is fully archive-driven — no
                                   local metadata needed (docs/overview/14-lifecycle.md).
                                   A sealed archive is authenticated whole
                                   before anything stops or is written
                                   (a damaged or tampered one: 502, "the
                                   sealed archive is damaged or was
                                   tampered with"). Its key erased: "this
                                   backup's data was erased on <date>
                                   (<reason>)" — a split main archive whose
                                   data key alone was erased restores its
                                   source and terminal layer, and the answer
                                   gains dataErased: "<why>", without "data"
                                   in restored. One whose data archive is
                                   gone though its key isn't erased (the
                                   archiver lost it), or that names a
                                   version no URL can, restores the same
                                   way and answers dataMissing: "<why>" (a
                                   data file of it: 404). A data archive of
                                   another backup, or sealed under another
                                   key, in its place is refused (502,
                                   nothing written). A key this workspace
                                   never had: "this backup was sealed by
                                   another workspace: import its keys (bx
                                   backup keys import)".
                                   {ok, component, restored:[parts],
                                   sandboxesSkipped?:[…]} — a manager's sandbox
                                   definitions come back by uid, stopped (§Tile
                                   sandboxes, "The workspace around them"); the
                                   ones left out, and why, are listed.
                                   The archiver is chosen by the @archive binding:
                                   bindings["<comp>"] override, else bindings["*"]
                                   default (set via POST /bindings). An archive
                                   whose deployment state belongs to another
                                   tile is refused before anything is written;
                                   this xbind leaves an archive's deployment
                                   record and checkpoint store out (logged):
                                   a tile restored where none stood comes
                                   back with plain live reload. The
                                   registration files it carries are
                                   validated row by row and merged into the
                                   deployments the tile has. When the
                                   archive lists deployment archives, each
                                   is restored by replace into the tile's
                                   deployment of its name, and the answer
                                   gains deployments: {restored: [names],
                                   skipped: ["<name>: <why>"]}
GET    /backup-schedule            admin. {schedules:[{component,schedule,retention}]}
POST   /backup-schedule            admin. body {component, schedule, retention} —
                                   owner-scheduled backup on the cron engine;
                                   retention prunes to N newest versions per run.
                                   A sealed workspace's data archives go with
                                   the main archives that name them: each run
                                   deletes the data versions no kept main
                                   archive names (never a count of their own).
                                   Each person's partition's archive keeps
                                   its own newest N; one of a partition
                                   that is gone (swept, reset, purged,
                                   switched away) loses every version once
                                   its key is erased — at an archiver
                                   without POST /archive/erase too (xbind
                                   keeps an index of the partition archives
                                   it wrote: data/backup-refs/partitions/).
DELETE /backup-schedule?component= admin. remove a component's schedule
GET    /backup-keys                admin. {mode, keys, unexported, lastExport?,
                                   exports, erased, erasedSinceExport,
                                   passphraseChanged?} — mode sealed |
                                   plaintext (the plaintext-vault mode:
                                   archives are plain tars) | vault-sealed
                                   (no backup until unsealed) | vault-locked
                                   (no vault set up yet: no backup until it
                                   is); unexported counts the backup keys no
                                   exported bundle since the last passphrase
                                   change holds; passphraseChanged is when
                                   the passphrase changed after the last
                                   export (POST /vault-rekey): every bundle
                                   before it is stale. The /alerts kind
                                   backup-keys, bx doctor and the admin
                                   console's Backup tab nudge on both.
POST   /backup-keys/export         admin, as a person: their own session or
                                   the admin tile's frame under their login
                                   (a tile's backend, terminal or agent:
                                   403, whatever its tile holds). The
                                   disaster-recovery key bundle {schema:1,
                                   workspace, created, barrier, keys:[…],
                                   erased:[…]}: the vault's barrier
                                   descriptor (the data key wrapped under
                                   the passphrase), every backup key file
                                   (wrapped under the data key) and every
                                   erase tombstone. Nothing without the
                                   vault passphrase; with the passphrase in
                                   force at export it opens the data key
                                   itself — every vault secret and all data
                                   at rest, not only the backups — and a
                                   later passphrase change doesn't take
                                   that back. Recorded as an export when
                                   answered, whether or not the client
                                   saves it. 409 without a vault barrier.
POST   /backup-keys/import         admin, as a person (as export). body
                                   {bundle, passphrase} — the bundle another
                                   workspace exported, and that workspace's
                                   vault passphrase: its data key is
                                   unwrapped in memory only, each backup key
                                   re-wrapped under this vault (never used
                                   to seal new archives), and its tombstones
                                   taken (a previously imported key they
                                   name is erased; this workspace's own keys
                                   never are). {imported, present, erased}.
                                   400 for a wrong passphrase or a bad
                                   bundle (nothing is imported; a
                                   descriptor over 1 GiB, 16 threads or 64
                                   passes is refused); needs this vault
                                   unsealed.
POST   /backup/erase               admin, as a person (as export). body
                                   {component, what: "data"|"all"} —
                                   crypto-erase a tile's backups: data
                                   deletes its data keys (main's and every
                                   deployment's namespace: the source stays
                                   restorable), all every key of the tile.
                                   It waits for the tile's backups in
                                   flight. Each key is tombstoned first (a
                                   restore then says why), then its file
                                   deleted; the subject gets a new key at
                                   its next backup, and the archiver is
                                   asked to delete the versions sealed under
                                   them (POST /archive/erase; an archiver
                                   without it keeps them, unreadable, until
                                   retention prunes them). {ok, component,
                                   erased:[{id, subject, gen}], archiver:
                                   "<what the archiver did>", note: "<what
                                   no key erases: plain archives made before
                                   sealing; a tile that doesn't root its
                                   scope names the root holding its data>",
                                   error?: "<a key file not removed yet —
                                   erased all the same>"}. data takes
                                   people's partitions' keys (part:) too.
                                   A tile with a partition mode record gets
                                   a history entry {op: "backup-erase", by,
                                   at, reason, wiped: {subkeys}}, as does
                                   every erase of a person's partition's key
                                   (its partition id in partition): its
                                   sweep, reset or purge. Of the history's
                                   200 entries backup ones (backup-erase,
                                   partition-restore) take 100 at most, the
                                   oldest going first: they never push a
                                   manager's act out.
GET    /partitions/backups?tile=…  the person (their own partition), in
       [&user=…][&partitionId=…]   their own session, app or device; an
                                   admin (anyone's: the root token, or the
                                   admin tile's frame under their login).
                                   Every tile principal else — a partition's
                                   own code, frames, terminals, agent
                                   sessions — and view-as: 403. The
                                   archiver's versions of user's (default:
                                   the caller) partition archive of tile
                                   (.partitions.<tile-key>.<dep>.<partition
                                   id>, dep the primary); partitionId
                                   (u-<32 hex>) names an earlier holder's
                                   (the id deleted and recreated since),
                                   default the person's current one. A
                                   person names only their own current id:
                                   any other 403, before the archiver is
                                   asked, never saying whose it is.
                                   {tile, partition: "user:<id>",
                                   partitionId, deployment, partitioned,
                                   archiver, versions:[{version,time,size}]};
                                   404 for no such person or tile.
POST   /partitions/restore         the person (their own partition) or an
                                   admin, as GET /partitions/backups. body
                                   {tile, user?, partitionId?, version?,
                                   confirm, to?, dryRun?} — replace user's
                                   partition of tile (its namespace's data,
                                   vault and registrations; its record stays
                                   the person's current one) with a version
                                   of its archive (default the latest),
                                   after confirm: "<tile> user:<id>", typed
                                   (400 {error, confirm} otherwise). Its
                                   instance stops and its namespace is held
                                   meanwhile; it starts again on next use.
                                   Only a sealed partition archive sealed
                                   under that partition's own backup key, of
                                   the same tile and user id, restores (409
                                   otherwise, nothing written). The same
                                   incarnation (the archive's uid is the
                                   person's now) restores by the person or
                                   an admin; an earlier holder's
                                   (partitionId) only by an admin with to:
                                   "<id>" (the same id again: audited, the
                                   person is told — push kind
                                   tile.partition-restored) — a person 403,
                                   without to 400; never into another id
                                   (403, and 400 for a to naming another).
                                   409 while the tile isn't partitioned
                                   now, while it is paused, for a key
                                   erased ("this backup's data was erased
                                   on <date> (<reason>)") or another
                                   workspace's ("… import its keys").
                                   Everything is judged again under the
                                   tile's backup lock: a switch, reset,
                                   purge, sweep or erase that ran while the
                                   restore waited wins (409 "… it changed
                                   while this restore waited; nothing was
                                   restored"). →
                                   {ok, tile, partition, partitionId, from,
                                   version, resources, earlierHolder, data,
                                   vault, registrations:[files], skipped?,
                                   vaultSkipped?} — skipped: archived
                                   resources the tile no longer keeps per
                                   person; vaultSkipped: a vault another
                                   vault sealed (another workspace's
                                   archive), left out. dryRun checks it all
                                   and writes nothing (no confirm). The
                                   tile's history gains {op:
                                   "partition-restore", by, at, reason,
                                   partition}. Audited.

Tile deployments: pausing live reload, a tile's deployments, promotion
(docs/tile-deployments.md; conventions and texts: §Tile deployments below
this block). features lists what this xbind speaks: live-reload/1 (pausing
live reload, the log, the diff, deploy, roll back, the checkpoint remote)
and deployments/1 (every other row). An older xbind answers a plain 404 or
405, which is how a client detects the feature; an act this build of xbind
can't do answers 501 ({"error","docs"}), and its Can reads "not built in
this xbind yet". Bodies are JSON, decoded strictly; each names `tile`, a
tile ref (apps/crm, or apps/crm+dev for one deployment; a body's deployment
and the ref's qualifier must agree). The GET rows take `tile` as a query
parameter: a tile's path, never a ref — a deployment is named with
`deployment=` (D127j; §Tile deployments, *Tile refs in a query string*). Each
body takes dryRun:true (judged as for real, refusals included, nothing
changes → {state, impact}) and seq (the record's sequence the caller acted
on: 409 when it moved; optional everywhere). A dry run of a deploy, roll
back or promote onto the primary whose code asks for another partition
mode than the tile records carries impact.partition, a warning: the tile
will pause for a partition-mode decision (it holds data), the mode follows
at once (it holds none), or a manager declined that mode (docs/partitions.md
§The mode). confirm tokens guard data: remove "erase", reset and a restore into data "erase-data", seed
and add with data:"seed" "copy-data", primary "data-stays" — a missing one
is 400 naming it. An operation answers {state, deploy?, …} (each row names
its answer) once the record change is committed; deploys are asynchronous
(follow the deploy entry with the log's ?id= and `deployments` events). On a
tile without deployments nothing here writes a file, except the three
opt-ins (live-reload/pause, add, protect) and purge, which creates nothing;
any other POST on it answers 409. Onto a protected primary every code move
names the checkpoint its actor reviewed (checkpoint on deploy and rollback;
expect on live-reload/now, promote and primary; plus seq), else 400 before
anything is captured, dry runs included; restart:true needs neither.
"tile manager" below is a person's own session — the tile's owner, its
org's admins, a workspace admin; the root token (bx on the host) is one —
never a terminal, agent or tile credential, whatever grants its tile holds.
One exception (D127m): on primary, protect, deliveries
and always-on, a frame of a tile holding the `xbin` admin capability (the
admin console, `tiles/admin`) whose token was minted under a person's own
login — a session or the root token, not a view-as session, not a token a
terminal or agent session minted — stands in for that person, and the person
is judged as a tile manager. For a tile its person manages that frame is
also the write audience of the reads: GET /deployments in the full view,
the log, and a diff without a work-tree side (not `deployments` events,
which it gets as any frame does). Every other manager route, and
every code move, refuses it like any tile credential (docs/auth.md §Tile
deployments).

GET    /deployments?tile=<tile>&deployment=<name>
                                   read on the tile, or the tile itself
                                   (readers get the primary only). → State
                                   in the caller's view: full (the tile's
                                   writers), deployment (a non-primary
                                   deployment's own credentials: the
                                   primary and that deployment) or reader
                                   (facts about the primary only — no other
                                   deployment's name, no count).
                                   record:false for a tile without
                                   deployments, and nothing is written.
                                   features lists what this xbind speaks
                                   (live-reload/1, deployments/1; none
                                   while --tile-deployments=off).
                                   deployment= selects one, echoed as
                                   selected; one naming a non-primary
                                   deployment is 403 for a caller outside
                                   its audience, whether or not it exists,
                                   and 404 (no such deployment) for the
                                   rest when it doesn't exist; a malformed
                                   name is 400. tile= is a
                                   tile's path: a '+' in it that names no
                                   tile, escaped or read as a space, is
                                   400 (D127j)
GET    /deployments/log?tile=<tile>&deployment=<name>&limit=<n>&before=<id>
                                   write on the tile, or its terminal/agent
                                   sessions. → {tile, entries:[DeployEntry],
                                   more}: every
                                   attempt, newest first — queued and
                                   running ones included, and failed ones
                                   (limit default 50, max 200); without
                                   deployment every
                                   deployment's entries, by id;
                                   ?id=<id>&wait=<s> one attempt,
                                   held until it finishes (wait ≤ 25) →
                                   {entry}. A non-primary deployment's own
                                   credentials see its entries only;
                                   readers and the primary's frame and
                                   backend tokens are refused
GET    /deployments/diff?tile=<tile>&from=<spec>&to=<spec>&path=<file>&stat=1
                                   write on the tile, or its terminal/agent
                                   sessions; a work-tree side captures a
                                   checkpoint and needs terminal
                                   level. spec: c:<id> | deployment:<name> |
                                   work-tree (default: the primary → the
                                   work tree). → text/x-diff with
                                   X-XBin-Checkpoint-From / -To (X-Truncated
                                   past 16 MiB), or stat=1 → {from, to,
                                   files:[{path, status, added, removed,
                                   binary?}], truncated}. Confined. 409 on a
                                   tile without deployments (nothing is
                                   captured), 429 while one diff runs and
                                   one waits for the tile, 504 past 30 s
POST   /deployments/live-reload/pause
                                   terminal-level on the tile (its own
                                   terminal/agent tokens count). {tile} →
                                   {state, deploy?}: saves stop
                                   reaching the live reload target, which
                                   keeps a fresh checkpoint of the work
                                   tree. The opt-in that creates a tile's
                                   record. Idempotent. 409 for a backend
                                   tile without --isolate
POST   /deployments/live-reload/resume
                                   terminal-level on the tile. {tile,
                                   deployment?, confirm?} → {state, deploy}:
                                   deployment (default: where live
                                   reload last was) follows the work tree
                                   again. Onto main, while it is the only
                                   deployment and every setting is at its
                                   default, the record is removed
                                   (record:false). 409 while live reload is
                                   on another deployment (attach instead),
                                   and onto a protected primary
POST   /deployments/live-reload/now
                                   terminal-level on the tile. {tile,
                                   expect?, confirm?} → {state, deploy?}: checkpoint
                                   the work tree and deploy it
                                   once where live reload last was, which
                                   stays pinned (unchanged:true when nothing
                                   moved). Onto a protected primary: a tile
                                   manager, with expect and seq
POST   /deployments/live-reload/attach
                                   terminal-level on the tile. {tile,
                                   deployment, confirm?} → {state, deploy}: the work
                                   tree is checkpointed, the former target
                                   is pinned to that checkpoint (its deploy
                                   entry is how attach too), and deployment
                                   follows the work tree. Idempotent. 409
                                   "live reload is paused: resume it onto
                                   <name> instead", and onto a protected
                                   primary
POST   /deployments/add            terminal-level on the tile.
                                   {tile, deployment, from?: work-tree|
                                   primary|c:<id>, data?: empty|seed,
                                   attach?, confirm?, branch? | newBranch?}
                                   → {state, deploy, joins?}: a new
                                   deployment with its code
                                   (default a fresh checkpoint of the work
                                   tree), its own data (empty; seed —
                                   confirm:"copy-data" — and joining data
                                   already seeded, restored or partly so are
                                   a tile manager's acts), a vault holding
                                   the primary's key names without values,
                                   deliveries on, alwaysOn off, limits at
                                   the tile's. Its code is built in the
                                   background and its process starts on its
                                   first request. attach:true (from
                                   work-tree only; 400 otherwise) moves live
                                   reload onto it. data:"seed" seeds it once
                                   it is added (409 on a workspace-scope
                                   tile, whose data has one namespace); a
                                   seed refused then leaves it added, empty.
                                   joins: the (scope, name) data a sibling
                                   tile already claims, with its state and
                                   who seeded or restored it. A dry run on a
                                   tile without deployments captures nothing
                                   (impact.code null). An opt-in. A name is
                                   lowercase letters, digits and -, a letter
                                   first, at most 24; not main. 409 "<tile>
                                   already has a deployment "<name>"", "a
                                   tile exists at <tile>+<name>; pick another
                                   name", "<tile>'s name holds '+', which
                                   names a tile deployment in URLs: it can't
                                   get deployments — clone it to a path
                                   without '+' (POST /api/xbin/clone) to give
                                   it some" (D127j), "<tile> has <n> non-primary
                                   deployments, the most allowed here", "the
                                   workspace has <n> non-primary deployments,
                                   the most allowed here", "<tile> can't
                                   have non-primary deployments: <reason>"
                                   (workspace chrome, code that asks for
                                   chrome, an xbin or xbin:* grant), and for
                                   a backend tile without --isolate.
                                   branches/1: branch assigns it a branch
                                   (*Assigned branches* below); newBranch
                                   creates that branch in the tile first
                                   and checks it out (409 "<tile> already
                                   has a branch <b>: newBranch only creates
                                   one …"; a dry run creates nothing). Code
                                   from a work tree on another branch than
                                   the one assigned needs confirm
                                   "other-branch" ("copy-data,other-branch"
                                   with data:"seed")
POST   /deployments/remove         terminal-level on the tile.
                                   {tile, deployment, confirm:"erase"} →
                                   {state}: stops it and ends its queued and
                                   running deploys; deletes its vault, logs,
                                   registrations, build products, and its
                                   data when this tile is the last of its
                                   scope with that deployment (a sibling
                                   keeps it otherwise); checkpoints stay
                                   until GC. Live reload attached to it
                                   detaches, and lastLiveReload falls to the
                                   primary. 409 "main can't be removed",
                                   "<name> is the primary of <tile>"
POST   /deployments/deploy         terminal-level on the tile. {tile,
                                   deployment, checkpoint? | expect? |
                                   restart?, confirm?} → {state, deploy?}: puts the
                                   checkpoint (default: a fresh one of the
                                   work tree, equal to expect when given) on
                                   deployment; live reload attached to it
                                   pauses. restart:true starts a new
                                   generation of its current code and clears
                                   the crash breaker (terminal level on
                                   every deployment, a protected primary
                                   included: it moves no code). Onto a
                                   protected primary: a tile manager, with
                                   checkpoint and seq
POST   /deployments/promote        as deploy, for the target.
                                   {tile, from, to, expect?} → {state,
                                   deploy?}: to receives from's current
                                   code (its checkpoint, or a fresh
                                   checkpoint of the work tree while from
                                   follows it) — code only, data stays; the
                                   entry carries from. Live reload attached
                                   to to pauses. Onto a protected primary
                                   expect and seq
POST   /deployments/rollback       as deploy. {tile, deployment,
                                   checkpoint?} → {state, deploy?}: default
                                   the newest ok entry of its deploy log
                                   with other code; data stays. Onto a
                                   protected primary checkpoint and seq
POST   /deployments/primary        tile manager. {tile, deployment,
                                   confirm:"data-stays", expect?} →
                                   {state, deploy?, inactiveHosts?}: the
                                   bare URL moves to deployment; data and
                                   xbin.json don't, limits stay with each
                                   deployment. The old primary's interface
                                   instances and ingress hosts go dormant
                                   at once and the new one's activate (cron
                                   jobs and bus subscriptions stay with the
                                   deployment that registered them); the
                                   answer returns
                                   once routing moved, and both
                                   generations restart after it, the new
                                   primary first; sessions named after
                                   either restart. Onto a protected primary
                                   (expect and seq), a target that follows
                                   the work tree is first pinned to exactly
                                   expect (deploy entry how "reassign").
                                   inactiveHosts: its ingress hosts that
                                   conflict with another tile's and stay
                                   off. Idempotent. 409 "<name> isn't
                                   healthy (<state>); only a healthy
                                   deployment can become the primary", and
                                   "reassigning the primary of <tile> would
                                   split <scope>'s data: …" unless the tile
                                   is in the workspace scope or alone in the
                                   scope it roots; "<tile> is partitioned:
                                   switching the primary would leave every
                                   person's data with <primary>: promote
                                   instead" when its recorded partition
                                   mode has user partitions
POST   /deployments/protect        tile manager. {tile, on, expect?} →
                                   {state, deploy?, nested?, warnings?}: on
                                   pins the primary in place (while live
                                   reload is on it, to a fresh checkpoint,
                                   equal to expect when given; how
                                   "protect"); from then on only tile
                                   managers change its code, naming the
                                   checkpoint they reviewed, and sessions
                                   following it restart onto the default
                                   target (§/ws/term). off: nothing moves;
                                   the primary stays pinned. nested (dry
                                   runs too): the components nested in the
                                   tile, [{tile, protected, manager}], whose
                                   code its writers change; warnings: one
                                   per unprotected nested component, and
                                   "not enforced: authentication is off"
                                   under --no-auth. An opt-in. Idempotent.
                                   off answers 409 (kind policy) while the
                                   tile, or a partitioned tile it is bound
                                   into, runs reviewed code only (POST
                                   /partitions/reviewed)
POST   /deployments/edge           tile manager. {tile, edge:
                                   slot:<slot>|grant:<target>, policy: read|
                                   block|inherit|default} → {state}: that
                                   edge's policy for every non-primary
                                   deployment of the tile, at their next
                                   call; default removes the override (also
                                   one whose edge is gone). The values an
                                   edge takes are its Edge.values (§Tile
                                   deployments, *The edge policy*). 400
                                   "<edge> takes <values>: <reason>", 404
                                   "<tile> has no edge <id>"; a net slot
                                   sharing the host's network takes inherit
                                   but stays block (effective + why).
                                   Idempotent
POST   /deployments/deliveries     tile manager. {tile, deployment, on} →
                                   {state}: the off switch of its cron jobs
                                   and bus push subscriptions, on by
                                   default. on:false keeps them registered
                                   but dormant (no tick, no delivery);
                                   on:true makes them fire for it again.
                                   Interface instances and ingress hosts
                                   never activate off the primary either
                                   way. 409 for the primary, whose
                                   registrations are always active
POST   /deployments/always-on      tile manager. {tile, deployment, on} →
                                   {state}: it is kept up — restarted after
                                   an exit on its own backoff, never reaped
                                   — while its own code declares alwaysOn
                                   and this switch is on. 409 for the
                                   primary, and when its code doesn't
                                   declare alwaysOn
POST   /deployments/limits         tile manager. {tile, deployment,
                                   limits:{memMiB?, pids?, diskGiB?}} →
                                   {state}: each override lowers the tile's
                                   ceiling for this deployment; null
                                   removes one, an absent key stays.
                                   memMiB and pids apply to its cgroup leaf
                                   from its next generation; diskGiB is the
                                   quota of its data (the lowest among the
                                   scope's tiles that have it), set on the
                                   scope's root tile only. 400 "<limit>
                                   can't exceed the tile's ceiling (<n>)" or
                                   not a positive integer; 409 "the quota
                                   of <scope>'s "<name>" data is set on
                                   <root tile>" elsewhere, the workspace
                                   scope included
POST   /deployments/seed           tile manager of every tile that has the
                                   deployment. {tile, deployment,
                                   confirm:"copy-data", stop?} → {state}:
                                   its data, replaced by a copy of the
                                   scope primary's (§Tile deployments, *Data
                                   per deployment*). Answers once the data
                                   is held (data.busy "seeding");
                                   completion is a deployments event, op
                                   data. 409 for the primary, while another
                                   data act runs, when the primary's data is
                                   over the target's disk limit or the disk
                                   is low, and when stop:true is required;
                                   503 while the vault is sealed
POST   /deployments/reset          terminal-level on every tile of the
                                   scope that has the deployment (main,
                                   while it isn't the primary: a tile
                                   manager's). {tile, deployment,
                                   confirm:"erase-data", vault?} → {state}:
                                   empties its data (data.state empty,
                                   reset:true); vault:true also empties its
                                   vault back to placeholders. Never data
                                   some tile serves as its primary, never
                                   res:workspace/*. 403 "<operation> of
                                   <scope>'s "<name>" data needs <level> on
                                   <tile> too"; 409 for the primary and while
                                   another data act runs
POST   /deployments/vault-copy     tile manager. {tile, deployment, keys? |
                                   all?} (exactly one) → {state, copied,
                                   missing}: the primary's values overwrite
                                   its keys, never the other way; the audit
                                   names keys, never values. 409 for the
                                   primary; the vault routes' 503 while
                                   sealed
POST   /deployments/backup         admin in a person's own session. {tile,
                                   deployment} → {ok, deployment, version}:
                                   archives its data now, through the tile's
                                   bound @archive provider, under its own
                                   archive key (POST /backup is unchanged).
                                   502 with no archiver bound
GET    /deployments/backups?tile=<p>&deployment=<name>
                                   admin (as above) → {deployment,
                                   versions:[{version, time, size}],
                                   archiver}: empty versions without an
                                   archiver, as GET /backups. deployment
                                   defaults to the primary; a removed
                                   deployment's archives are still listed
POST   /deployments/restore        admin (as above), and the reset level on
                                   every tile claiming the target's data.
                                   {tile, deployment, version?, into?,
                                   replace?, confirm?} → {ok, deployment,
                                   into, restored, skipped}: data only,
                                   never the work tree (POST /restore is
                                   unchanged). deployment: whose archive
                                   (main: the data part of the tile's — of
                                   a sealed workspace's, the data archive
                                   its main archive names; its key erased:
                                   409 "this backup's data was erased on
                                   <date> (<reason>)");
                                   version: default latest; into: the
                                   target, default the archive's own.
                                   Beyond main each archived resource is
                                   emptied first; main merges unless
                                   replace:true. confirm:"erase-data" only
                                   when the target holds data; skipped
                                   names archived resources the target
                                   doesn't declare. Stops every deployment
                                   using the target's data; data.state
                                   becomes restored. 409 when the target
                                   doesn't exist (the answer lists the
                                   choices) or its data is busy, and for an
                                   archive older than the tile's last
                                   partition mode switch that deleted its
                                   data (main's; a deployment's after one
                                   that deleted everything,
                                   docs/partitions.md §Backups): this route
                                   can't confirm it — only POST /restore,
                                   the whole tile's (its source and every
                                   deployment's data that backup lists),
                                   takes the switch's date
POST   /deployments/backup-schedule
                                   admin (as above). {tile, deployment,
                                   schedule, retention?} → {state}: a
                                   non-primary deployment's data on a
                                   5-field cron schedule, keeping retention
                                   versions (default 3); schedule ""
                                   removes it
POST   /deployments/run-now        terminal-level on the tile. {tile,
                                   deployment, job} → {state,
                                   delivery:{status, ms}}: delivers the job
                                   once to that deployment as xbin/cron
                                   with the job's role, whatever its
                                   deliveries switch says, and waits for the
                                   handler (≤ 2 min); status is what it
                                   answered. 409 for the primary (its jobs
                                   fire on schedule) and "<job> is already
                                   running on <name>"; 502 when its backend
                                   can't start
POST   /deployments/purge          tile manager. {tile, checkpoint:"c:<id>"}
                                   → {state, purged, entries}: every deploy
                                   log entry of the tile naming the
                                   checkpoint is rewritten to name none
                                   (entries: how many), and its content,
                                   git view and materialized tree are
                                   pruned at once; content other
                                   checkpoints share stays, archives made
                                   earlier keep it. No confirm token: the
                                   id names what goes. Accepted on a tile
                                   without deployments, whose store
                                   outlives the opt-out. 409 "<tile>:
                                   checkpoint <id> is in use — <why>; only a
                                   checkpoint no deployment runs can be
                                   purged" (a deployment runs it, a deploy of
                                   it hasn't finished, a running generation
                                   binds it); 404 a checkpoint the tile
                                   doesn't have
POST   /deployments/branch         terminal-level on the tile. branches/1.
                                   {tile, deployment, branch: "<b>"|null}
                                   → {state}: the work tree's branch the
                                   deployment requires (*Assigned
                                   branches* below); null clears it, and
                                   either drops its override. branch is
                                   required (400 "bad request body: branch
                                   is required …"); a name xbind doesn't
                                   take is 400. The dry run's impact.branch
                                   names the work tree's branch, and
                                   pausesLiveReload says live reload on it
                                   pauses at the next save. 409 "main takes
                                   no assigned branch …", "<name> is the
                                   primary of <tile> — the primary takes no
                                   assigned branch". Idempotent
GET    /checkpoints/<tile>.git/<path>
                                   the tile's terminal/agent sessions, or
                                   write on the tile. Read-only dumb HTTP
                                   git (git fetch xbin-deploy): the tile's
                                   view repository —
                                   refs/heads/deploy/<name> per pinned
                                   deployment and HEAD naming the primary's,
                                   nothing else. path: HEAD, info/refs,
                                   objects/info/packs, a pack or a loose
                                   object; anything else 404

GET    /vault-status              admin. {initialized, sealed, mode, insecure}
                                   mode: unsealed|sealed|unconfigured|plaintext
POST   /vault-rekey               admin. body {current, new} — change the
                                   passphrase (re-wraps the data key; needs
                                   unsealed + the current passphrase).
                                   Every backup key bundle exported before
                                   is stale from then on (GET /backup-keys
                                   passphraseChanged, the backup-keys alert)
POST   /vault-unseal              admin. body {passphrase} — unseal (or init
                                   the barrier on first use). {created}
POST   /vault-seal                admin. drop the key from memory
GET    /vault/<component>          backend/terminal self, or admin. {keys:
                                   […]} — list keys (admin
                                   may list any vault)
GET    /vault/<component>/<key>    BACKEND ONLY (instance token, D30).
                                   {value} — a secret's value is readable
                                   only by the owning tile's backend;
                                   admins AND the tile's own terminals get
                                   403 (they list + set, never read)
PUT    /vault/<component>/<key>    backend/terminal self, or admin. body
                                   {value} (write-only management — D30;
                                   frame tokens can't reach the vault API)
DELETE /vault/<component>/<key>    backend/terminal self, or admin.
                                   (all vault get/set → 503 when sealed)
                                   Each deployment of a tile has its own
                                   vault. ?deployment=<name> on each vault
                                   route: a tile's own credentials act on
                                   their bound deployment's vault (a backend
                                   on its deployment's, a terminal or agent
                                   session on its target's; naming another is
                                   403); admins and tile managers, in their
                                   own session, name any deployment of the
                                   tile, except that a tile manager who
                                   isn't an admin reaches only non-primary
                                   deployments' vaults; other tiles' admin
                                   credentials reach only the primary's. A
                                   named deployment is echoed as deployment.
                                   A non-primary deployment's list is {keys,
                                   placeholders}: the primary's key names it
                                   has no value for, computed on each
                                   request; reading one → 404 "vault key
                                   "<k>" has no value in deployment <d>; a
                                   tile manager can copy it from the
                                   primary" (POST /deployments/vault-copy).
                                   Values stay readable only by the backend
                                   of the deployment whose vault it is. While
                                   the primary is protected its vault is
                                   written only by its own backend and by
                                   tile managers in their own session.
                                   A partitioned tile's (docs/partitions.md
                                   §Vault and registrations): its own
                                   credentials acting in a person's
                                   partition — that partition's backend, the
                                   person's terminals and agent sessions —
                                   reach that partition's own vault (values:
                                   its backend only); everyone else, admins
                                   included, reaches the global instance's.
                                   ?partition= on a partitioned tile's vault,
                                   cron and bus-subscription routes → 400 "a
                                   partition's vault and registrations are
                                   reached only from inside it: …", for
                                   everyone; ignored on other tiles. While
                                   the tile is paused (its partition mode
                                   pending or invalid, or a switch deleting
                                   its data) a partition's vault writes →
                                   409

GET    /kv/res:<scope>/<name>/?prefix=   reader. {keys}
GET    /kv/res:<scope>/<name>/<key>      reader. raw bytes
PUT    /kv/res:<scope>/<name>/<key>      writer. body = value (≤1 MiB). 507 if
                                         the scope is over its disk quota
DELETE /kv/res:<scope>/<name>/<key>      writer.

GET    /blob/res:<scope>/<name>/[path]   reader. file bytes | {entries} for dirs
PUT    /blob/res:<scope>/<name>/<path>   writer. body = content (≤256 MiB)
DELETE /blob/res:<scope>/<name>/<path>   writer.

POST   /bus/publish                      writer on the resource.
                                         body {resource, topic, data?}
                                         Partitioned tiles (partitions.md):
                                         in a partitioned scope these kv,
                                         blob and bus routes reach the
                                         namespace the caller's partition
                                         holds: a person's own partition
                                         (their session, and the tile's
                                         frames, terminals and backend in
                                         their partition), the global
                                         instance's at today's keys, and a
                                         shared resource's ("shared": true |
                                         "read") at today's keys for
                                         everyone. An event on a partitioned
                                         scope's own (not shared) bus carries
                                         "partition" — the publisher's,
                                         "global" or "user:<id>" — and
                                         reaches only subscribers acting in
                                         that partition; a shared bus's
                                         carries none. Their answers add:
                                         403 "res:<scope>/<name> is
                                         read-only for people's partitions"
                                         on a write (kv PUT/DELETE, blob
                                         PUT/DELETE, bus publish) by a user
                                         partition to a "shared": "read"
                                         resource; 403 when the caller
                                         reaches no partition of the scope
                                         (view-as; a credential without a
                                         person on a tile without a global
                                         instance; an unpartitioned tile
                                         when the scope has no global
                                         instance), and, from a user
                                         partition of another partitioned
                                         tile holding the grant, "<id> can't
                                         read <tile>: …" when the person
                                         can't read the tile, or "<id>
                                         hasn't let <caller> use their
                                         <tile> data" while the workspace's
                                         partitionConsent policy is on and
                                         they haven't consented; 409 while
                                         the scope's tile is paused (a
                                         pending or invalid partition mode)
                                         or its mode record can't be read —
                                         the data isn't reached through
                                         today's keys meanwhile; 507 when
                                         the partition is over its own disk
                                         ceiling (by default the tile's
                                         per-namespace one): one person's
                                         data is write-blocked, not the
                                         tile's.
GET    /bus/subscriptions                own push subscriptions (admin: all),
                                         with counters since the daemon started.
                                         {subscriptions:[{name, resource, prefix,
                                         component, path, role, delivered,
                                         dropped, failed, lastError?, lastAt?}]}
                                         A tile credential lists its own
                                         deployment's; rows carry deployment
                                         (absent for main), dormant (absent
                                         while active) and dormantEvents;
                                         ?deployment=<name> (admin) lists that
                                         deployment's, echoed as deployment
PUT    /bus/subscriptions                reader on the bus resource (the
                                         SUBSCRIBER's grant, even when an admin
                                         registers it). body {name, resource,
                                         prefix?, path, role?, component?¹};
                                         idempotent by name; ≤64 per component;
                                         409 over the limit. Delivery: below.
                                         A tile credential subscribes its own
                                         deployment (one beyond main keeps its
                                         own subscriptions, ≤64 per
                                         deployment); while that deployment
                                         isn't the primary and its deliveries
                                         are off the subscription is dormant:
                                         200 {ok, dormant:true}.
                                         ?deployment=<name> (admin) subscribes
                                         that deployment (echoed); a tile
                                         credential naming another deployment
                                         → 403
DELETE /bus/subscriptions/<name>[?component=]  element: own; admin: any.
                                         ?deployment=<name> (admin): that
                                         deployment's
                                         On a partitioned tile its credentials
                                         acting in a person's partition list,
                                         subscribe and delete that partition's
                                         own (≤16, the 17th: 409; rows gain
                                         dormantDrops); on a partitioned
                                         scope's bus the partition must reach
                                         that scope — its person can read it
                                         and, with partitionConsent on,
                                         consented — else 403, and deliveries
                                         stop while it doesn't (the row is
                                         dormant); while the tile is paused
                                         (pending, invalid, switching) → 409;
                                         admins reach the global instance's
                                         only; ?partition= → 400 (the vault
                                         rows above)

GET    /tile-report                      any signed-in user (read-filtered).
                                         {statuses:{<component>:{level,message,ts}}}
                                         — status tiles reported about themselves,
                                         for the shell's sidebar/tab indicators.
                                         (Distinct from /tile-status = runtime
                                         metrics, above.)
POST   /tile-report                      element (self) or owner (?component=).
                                         body {level:ok|info|warn|error, message?,
                                         transient?}. Sets this tile's persistent,
                                         self-clearing status (ok+empty message
                                         clears it); transient=true fires a one-shot
                                         notification instead. Publishes a `status`
                                         event. SDK xbin.Status/Notify; JS
                                         xbin.status/notify. Cleared on backend
                                         restart. Guidelines: workspace AGENTS.md.
                                         From a non-primary deployment's
                                         credentials: the same answer; kept for
                                         that deployment and sent as a
                                         deployments event, op status
                                         {deployment, level, message, ts,
                                         transient}, never `status`; GET
                                         /tile-report never lists it. It clears
                                         when that deployment rebuilds or a
                                         deploy swaps onto it; a reassignment
                                         exchanges it with the primary's.
                                         From a user partition's credentials
                                         (partitioned tiles, §Authentication):
                                         kept for that partition, never the
                                         tile's status; its `status` event
                                         carries partition and reaches only
                                         that person (§/ws/events); GET
                                         /tile-report answers such a caller
                                         its partition's own record for its
                                         tile (none: no entry), and never a
                                         deleted person's to one created
                                         later under the same id. Cleared when
                                         that partition's instance restarts
                                         or the tile's code rebuilds

POST   /notify                           element: a tile's backend (instance
                                         token). body {user, title, body?, link?,
                                         kind?, collapseId?} → 202 {ok:true}. A
                                         push notification to a person's
                                         registered app devices (Push
                                         notifications, below). user: the id to
                                         notify ("user:<id>" accepted); they must
                                         be able to read the calling tile — 403
                                         otherwise, and for unknown or disabled
                                         users. A tile's frontend (frame token)
                                         or a shell in it (terminal token) may
                                         notify only the person using it (403
                                         for anyone else), and so may the
                                         backend of a person's partition of a
                                         partitioned tile (403 "a person's
                                         partition notifies only its person
                                         (<id>); …"); its global instance
                                         notifies any reader. link is relative to
                                         the tile (#fragment, ?query or a path
                                         inside it; no dot segments, encoded or
                                         not). kind (a–z 0–9 -) makes the push
                                         kind tile.<kind>; collapseId (≤64 of
                                         A–Z a–z 0–9 . _ : -) makes later ones
                                         replace earlier ones. 429 + Retry-After
                                         over the per-tile limit (a frontend's
                                         or terminal's: per tile and person, so
                                         a reader can't spend the backend's).
                                         202 also when
                                         push is off, the user has no device for
                                         the kind, muted the tile (nothing is
                                         counted then), or is over the per-user
                                         limit (dropped) — delivery is
                                         best-effort. SDK xbin.NotifyUser.
                                         From a non-primary deployment: 202
                                         {ok:true, suppressed:true}; nothing is
                                         pushed or counted, a deployments event
                                         op notify {deployment, to, title, at}
                                         reaches its developers, and its last
                                         20 are the deployment's wouldNotify
POST   /devices/push                     a signed-in person (the app's device
                                         session; not a tile). body {deviceId,
                                         handle, publicKey, kinds?,
                                         startHandle?} → {device: {deviceId,
                                         kinds, created, updated, lastSent?,
                                         pushToStart?, activities?},
                                         workspace, enabled}.
                                         Registers (or refreshes) where this
                                         user's pushes go: handle = the push
                                         relay's handle for this app install and
                                         workspace, publicKey = the device's
                                         X25519 key (base64url, 32 bytes), kinds
                                         = what it wants (agent, agent.permission,
                                         agent.question, agent.turn, tile,
                                         tile.<kind>; a kind matches those under
                                         it; none = all). One registration per
                                         (user, deviceId); a handle belongs to one
                                         registration (the newest). Bound to the
                                         login making it: a device session
                                         registers under its own device-login id
                                         only (403 otherwise) and the
                                         registration goes with the device; any
                                         other login's (the app before enrolling,
                                         a browser, the owner token) goes when
                                         that login ends, and can't take over an
                                         enrolled device's deviceId (409).
                                         workspace = the `ws` every payload
                                         carries; device.needsNewHandle (below).
                                         startHandle: the relay's Live Activity
                                         handle of the app's push-to-start
                                         token ("" removes it, absent keeps
                                         it); a new handle drops it and the
                                         device's Live Activities
GET    /devices/push                     a signed-in person. {workspace, enabled,
                                         devices:[{deviceId, kinds, created,
                                         updated, lastSent?, needsNewHandle?,
                                         relayError?, pushToStart?,
                                         activities?: [session]}]} — your own.
                                         needsNewHandle: the relay no longer
                                         delivers to that handle for this
                                         workspace (relayError handle_bound |
                                         handle_unknown) — the app creates a
                                         fresh handle and registers again
DELETE /devices/push/<deviceId>          a signed-in person: your own → 204 | 404.
                                         Its Live Activities end; the app
                                         registers again when next opened
POST   /devices/push/activities          a signed-in person, for a registration
                                         of theirs (a device session: its own
                                         deviceId, else 403). body {deviceId,
                                         session | ref, handle, since?} →
                                         {activity: {session, created,
                                         ended?}}. A Live Activity the device
                                         shows for one of your agent sessions:
                                         handle = the relay's Live Activity
                                         handle of its update token; ref = the
                                         one a push-started activity carries
                                         (the answer names its session); since
                                         = the turn's start the card shows
                                         (unix s; taken only for a turn xbind
                                         did not see begin). ended: the turn is
                                         over already — xbind sends the card
                                         its end and keeps no registration (a
                                         push-started card whose turn ended
                                         before its token came is answered so
                                         for 4 h). 404 for a session that is
                                         not yours or not there, an unknown
                                         ref, or a device with no registration
                                         (the app ends such a card); 429 over
                                         240/hour (burst 30). xbind then pushes
                                         the turn's state changes and its end
                                         to it (below)
DELETE /devices/push/<deviceId>/activities/<session>
                                         a signed-in person: the device stopped
                                         showing it (the user dismissed it) →
                                         204; xbind starts none there by push
                                         for the rest of the turn (noted even
                                         with no card registered). 404: no
                                         registration and no running turn of
                                         yours by that id
GET    /push/prefs                       a signed-in person. {mutedTiles:[path]}
PUT    /push/prefs                       a signed-in person. body {mutedTiles}
                                         (replaces; ≤1000) → the stored prefs.
                                         A muted tile's /notify reaches none of
                                         your devices
POST   /push/test                        a signed-in person. A test push to your
                                         devices (kinds don't filter it) → 202
                                         {devices}; 409 when push is off or no
                                         device is registered
GET    /push/config                      admin. {enabled, source?: env|admin,
                                         relay?, relayWorkspace?, keySet?,
                                         defaultRelay?, set?, by?, workspace,
                                         devices, staleDevices?, stats: {queued,
                                         sent, retried, failed, dropped, limited,
                                         lastError?, lastErrorAt?}, keyChecked?,
                                         keyError?} — the key is never shown; the
                                         relay stays listed while push is off.
                                         xbind checks the key in force with the
                                         relay at start and daily (the relay
                                         deletes keys nobody uses): keyChecked
                                         is when, keyError what it found (the
                                         relay forgot the key: for an admin key
                                         PUT {rotate:true}; for
                                         XBIN_PUSH_RELAY_KEY a new key)
PUT    /push/config                      admin. body {relay?, key?, rotate?}:
                                         turn push on. relay: https:// (http only
                                         on localhost; default: the stored relay,
                                         else XBIN_PUSH_RELAY). key: checked with
                                         the relay (400 when it does not know
                                         it). Without key: the stored key when
                                         the relay is the same (409 when the
                                         relay no longer knows it), else xbind
                                         registers the workspace with the relay.
                                         rotate:true registers anew — every
                                         handle that delivered under the old key
                                         then reads needsNewHandle until its app
                                         renews it. 409 when the environment
                                         configures the relay; 502 when the relay
                                         cannot be reached → the /push/config view
DELETE /push/config                      admin. Push off; the relay key and every
                                         registration stay, so PUT {} turns it
                                         back on with nothing to do in the apps
                                         → 204
GET    /push/devices?user=<id>           admin. {devices:[{user, deviceId, kinds,
                                         created, updated, lastSent?,
                                         needsNewHandle?, relayError?}]} — one
                                         user's registrations (without ?user=:
                                         every registration)
DELETE /push/devices/<user>              admin. Revoke all of a user's
                                         registrations → {removed} | 404
DELETE /push/devices/<user>/<deviceId>   admin. Revoke one (a lost phone) →
                                         {removed} | 404

GET    /cron/jobs                        own jobs (admin: all). {jobs}
                                         A tile credential lists its own
                                         deployment's; rows carry deployment
                                         (absent for main) and dormant (absent
                                         while active); ?deployment=<name>
                                         (admin) lists that deployment's,
                                         echoed as deployment
PUT    /cron/jobs                        writer on the cron resource.
                                         body {name, resource, schedule, path, role?, component?¹}
                                         A tile credential schedules for its
                                         own deployment (one beyond main keeps
                                         its own jobs); while that deployment
                                         isn't the primary and its deliveries
                                         are off the job is dormant: 200 {ok,
                                         dormant:true}. ?deployment=<name>
                                         (admin) schedules for that deployment
                                         (echoed); a tile credential naming
                                         another deployment → 403
DELETE /cron/jobs/<name>[?component=]    element: own; admin: any.
                                         ?deployment=<name> (admin): that
                                         deployment's
                                         On a partitioned tile its credentials
                                         acting in a person's partition list,
                                         schedule and delete that partition's
                                         own jobs (≤16, the 17th: 409; none
                                         more often than once a minute: 400;
                                         while the tile is paused — pending,
                                         invalid, switching — 409); admins
                                         reach the global instance's only;
                                         ?partition= → 400 (the vault rows
                                         above)
```

¹ `component` is owner-only; elements always schedule (and subscribe)
themselves.

**Bus push delivery (D85).** Each event published on a subscribed bus whose
topic starts with the subscription's `prefix` is POSTed to the subscriber's
`path` through the proxy, as `X-XBin-From: xbin/bus` with the subscription's
`role` (default `writer`) — starting an idle backend like a cron tick. The
JSON body:

```
{"id":"<event id>","subscription":"<name>","resource":"res:<scope>/<name>",
 "topic":"<topic under the resource>","data":…,"ts":<unix ms>}
```

`id` is the event's, the same for every subscription it reaches. Delivery
is at-most-once: one FIFO per subscription (256 events; the newest is
dropped when full), one POST in flight, 2 min timeout, no retries; more
than 100 events in one second are dropped (a loop guard). `reader` is
re-checked at each delivery (a revoked grant counts as `failed`); a
disabled or offloaded subscriber's events are dropped; a subscriber that
no longer exists loses the subscription. Answer 2xx; ≥400 counts as
`failed`. Stored in `data/bus-subscriptions.json`, carried in the
component's backup; a new tile created at a removed tile's path starts
without that path's cron jobs and subscriptions. A tile deployment beyond
`main` keeps its cron jobs and subscriptions in files of its own (§Filesystem
contract); a tick or delivery reaches the deployment that registered it, and
a dormant one (§Tile deployments) doesn't tick or receive anything (its
events count as `dormantEvents`, not `dropped`). A subscription on the tile's
own bus receives only events published in its deployment's data; one on
another scope's bus receives that scope's primary's, re-checked against its
edge at every delivery.

A person's partition's cron jobs and subscriptions (docs/partitions.md) are
kept in files of its own, never in `data/cron-jobs.json` or
`data/bus-subscriptions.json`, and a backup of the tile carries the global
instance's only. Their ticks and deliveries carry the partition
(`X-XBin-Partition: user:<id>`) and reach it while its person exists (the
same incarnation), is enabled and can read the tile — asked at every tick
and delivery, so regaining access resumes them. A tick whose start the
runner defers is tried again with jitter until the next tick is due. A
subscription receives its own partition's events (a partitioned scope's own
bus, its person's namespace) — which start the partition only when its own
tile published them — and a shared bus's, an unpartitioned scope's or one
another tile published for the person only while the partition runs
(skipped ones count as `dormantDrops`); never the global instance's. On a
partitioned scope's bus it is registered, and each event delivered, only
while the partition reaches that scope as a call of it would: its person can
read the scope's tile and, with the `partitionConsent` policy on, consented
— a shared bus needs the read access alone (403 at registration; a delivery refused meanwhile counts as
`dormantEvents`, its reason in `lastError`). A failed tick or delivery logs
its status, never the partition's answer.

**Partition mail doorbell** (docs/partitions.md §Partition mail). While an
inbox of a partitioned tile holds unacked items (`POST /partitions/mail`),
xbind POSTs to the tile's `partitionMail` path — declared beside `"global"`
in `xbin.json` — on the addressee's instance, through the proxy, as
`X-XBin-From: xbin/mail`, `X-XBin-Role: writer` and `X-XBin-Partition: <the
addressee>` (`global`, or `user:<id>` with its `X-XBin-Partition-Id`):

```
{"partition":"user:<id>" | "global","pending":<unacked items>}
```

The handler reads with `GET /partitions/mail` and acks; the answer's status
only is logged. A new item rings at once and starts the steps over; the
addressee's start (whatever starts it) rings again unless a ring was in
flight at it or came after it (the doorbell's own ring often started it);
and while items remain it rings after 1 min, 5 min, 30 min, 2 h, then every
6 h — a start doesn't shorten the steps. A person's stopped partition is started for it
— a background start that counts against the tile's mail starts (6 a
minute) — only when it has run before, and only while its person exists (the
same incarnation), is enabled and can read the tile; otherwise its items wait
for its next start. Nothing rings while the tile is paused, disabled or
declares no `partitionMail` (its code polls `GET`). A restart of xbind loses
no mail: boot rings every inbox holding items. The feature word is
`partition-mail/1`.

**Push notifications** (D94). The xbin app receives pushes through a push relay
(the repo's `relay/`, relay/README.md): it holds the APNs key, maps an opaque
handle to the device, and never sees content. An admin turns push on per
workspace (`PUT /push/config`, or `XBIN_PUSH_RELAY` + `XBIN_PUSH_RELAY_KEY`,
docs/config.md); the app registers each device with `POST /devices/push`.
Each notification is sealed to the device's X25519 key — ephemeral X25519,
HKDF-SHA256 (salt = ephemeral public key ‖ device public key, info
`xbin-push-v1`), AES-256-GCM — over the JSON payload `{v:1, ws, kind,
title, body, link, collapseId}`; the relay forwards the envelope `{v:1, epk,
n, ct}` inside a generic "New activity" alert and the app's extension shows
the real text (the exact format and test vectors: native/spec/push.md in
the repository). `link` is relative to the workspace: `c/<tile>/…` for a
tile, `agent/<session>` for an agent session, `xbin/<page>` for one of
xbind's own pages.

Sources: `POST /notify` (kind `tile` or `tile.<kind>`), and the agent
sessions of the device's user — a `permission.request` (`agent.permission`)
or `elicitation.request` (`agent.question`) still unanswered 3 s later, and
a `turn.end` that the user did not cancel (`agent.turn`), and xbind's own
partition notices: a tile's partition mode switch request to its managers
(`tile.partition-switch`, collapse per tile, at most one per tile every
15 minutes) and, after a switch, to each
person whose partition was deleted (`tile.partition-deleted`), both linking
`c/<tile>/`, and — with the workspace policy partitionConsent on — to a
person whose data in a partitioned tile another one's call was refused for
want of their consent (`tile.partition-consent`, at most one a day per
edge, collapse `partition-consent:<from>→<to>`, linking `xbin/partitions`:
the partitions page, not served yet — an app that doesn't know the link
opens the workspace), all spending the person's budget and ignoring tile
mutes. Limits (token
buckets): 120/hour per tile (burst 20) — a tile's frontend and terminals
have a bucket per tile and person, apart from its backend's; 240/hour per
user from all tiles together (burst 40); agent sessions have their own
240/hour per user (burst 40) and 120/hour per session (burst 20), so tiles
cannot crowd out a permission request; 60/hour (burst 10) of `POST
/push/test`; 30/hour (burst 10) of `POST /devices/push` per person (429).
The per-user budgets count relay posts — a notification costs one per
device it goes to (at most 10 registrations per person; the least recently
updated goes) — so one person's devices can't spend the workspace's relay
budget many times over. Over a per-user or per-session limit a push is
dropped (counted as `limited`). Nothing
reaches a disabled user. Signing a user out everywhere (`DELETE
/users/<id>/sessions`), disabling or deleting them drops their push
registrations (a deleted user's preferences too); removing an enrolled
device (`DELETE /devices/<id>`, `?devices=1` on sign-out-everywhere, a
password change with `removeDevices`) or its device session signing out
(`POST /logout` with its bearer) drops that device's registration — a
device session can register only under its own device-login id — and
rotating the owner token (`POST /auth-rotate-token`) drops the owner's. A
registration made by any other login (the app's password or SSO session
before enrolling, a browser session, the owner token) lasts as long as that
login: its logout, expiry or sign-out-everywhere drop it too. A
registration that goes ends its Live Activities (below). The app checks its
registration each time it comes to the foreground and registers again then
when it is signed in — so `DELETE /devices/push/<deviceId>` (the devices
panel's *remove*) stops notifications only until the app is next opened.
Delivery is asynchronous and best-effort: a bounded
queue (a full one drops), up to 5 attempts with backoff on relay 429/5xx or
network errors. Only the relay's own error codes touch a registration: 410
(the device is gone) removes it; 403 `handle_bound` or 404 `handle_unknown`
marks it `needsNewHandle` (skipped until the app renews the handle); any
other refusal — a proxy's 403, a wrong URL's 404, 401 `bad_key` — fails
that notification only (`lastError`). The relay key identifies this
workspace at the relay, and the relay binds each handle to the first
workspace that pushes to it: a new relay workspace (rotate, a changed
`XBIN_PUSH_RELAY_KEY`, another relay) orphans every handle that delivered
under the old one. State lives in `data/push/push.json` (mode 0600).

**Live Activities.** The app shows an agent turn on the lock screen and in
the Dynamic Island. It keeps the activity current itself while it runs;
otherwise xbind does, through the relay (`apns-push-type: liveactivity`).
An ActivityKit payload can't be sealed, so it carries generic state only —
`{phase: running | waiting | idle, since: <unix s the turn started>,
pending: <permission requests and questions waiting>}` — never a title,
name or text (the relay refuses anything else). xbind follows the agent
sessions of users with a registered device from their events: a change of
phase or pending count updates each registered activity of the session
(`POST /devices/push/activities`; priority 10 for waiting, else 5; stale
after 4 h without news), the turn's end ends them (idle, dismissed after 10
minutes) and drops their registrations — an activity is one turn — and so
does the session closing. A turn still running after 30 s starts an
activity by push (ActivityKit push-to-start) on each device whose
registration has a `startHandle`, takes `agent` kinds, and shows none for
the session (nor one the user dismissed this turn: `DELETE
…/activities/<session>`): the start carries only the workspace's push id and a random
`ref`, by which the app registers the new activity's token (if the turn
ended first, that registration answers `ended` and the card gets its end;
an unknown ref is 404 and the app ends the card itself). At start xbind
ends every activity still registered (agent sessions do not outlive it).
The relay's 410, `handle_bound` or `handle_unknown` on an activity's handle
drops that activity (or the push-to-start handle — and so does a start the
relay calls `bad_request`: a handle it does not hold as push-to-start); the
app registers the next one. A registration that goes (unregistered, the
device removed or signed out, sign-out-everywhere, an admin's revoke) ends
its activities at once. Updates are limited to 240/hour per activity (burst
30); one over the limit goes out, with the state then current, once the
limit allows. Wire formats: native/spec/push.md §7.

**Relay proof of work.** A relay may ask an anonymous workspace
registration for a proof of work (`xbin-relay -registration-pow <bits>`,
relay/README.md): xbind fetches its challenge (`GET
/v1/workspaces/challenge`), solves it — at most 28 bits; `PUT /push/config`
waits for it — and registers with it; a relay that asks for more answers
502 with a hint to get a key from its operator.

### Tile deployments (`/deployments`, `/checkpoints`)

The builder's view is [tile-deployments.md](/docs/tile-deployments.md);
this is the wire's. Every tile has one deployment, `main`, which starts as
its **primary**: what the bare URLs, grants, bindings, interface instances,
ingress and every other tile reach (a cron job or bus subscription reaches
the deployment that registered it). A tile's developers can pause live
reload, add deployments (`dev`, …) with their own URL, data, vault and deploy
log, promote code between them, and a tile manager can make another
deployment the primary or protect the primary. A tile that never opts in has
no deployment record, and every surface below answers for it exactly as
before.

**The state** (`GET /deployments`) depends only on who asks, never on how
many deployments exist. `view` is:
- `full` for the tile's write audience: admins, people with write (at their
  current level), and the tile's own terminal and agent sessions while their
  user has write;
- `deployment` for a non-primary deployment's own frame and backend
  credentials: the full view cut to the primary and that deployment. Other
  deployments are left out, `liveReload` and `lastLiveReload` read `""` when
  they name one of them, and `edges`, `caps`, `allowed` and `workTree` are
  omitted;
- `reader` for everyone else who may read the tile, the primary's frame and
  backend credentials included: the primary's facts only. `liveReload` reads
  the primary's name while it follows the work tree and `""` otherwise.
  `selected` (but for the primary's alias), `seq`, `lastLiveReload`,
  `liveReloadSince`, `workTree`, `allowed`, `caps` and `edges` are left out,
  and so is every other deployment. On the primary, `status.error`,
  `status.queued`, `data`, `vault`, `limits`, `deliveries`, `alwaysOn`,
  `alwaysOnDeclared`, `registrations`, `wouldNotify`, `created`, `by`,
  `can`, and the deploy entries' `id`, `how`, `from` and `session` are left
  out too.

A zero-state answer (`record: false`) is the same for every view but for
`workTree.branch`, which only the write audience gets, and reading it writes
nothing. `features` lists what this xbind speaks (`live-reload/1`,
`deployments/1`, `branches/1`; none while `--tile-deployments=off`): a
client sends a body field a feature added — `branches/1`'s `branch` and
`newBranch` on add, `confirm: "other-branch"`, the branch route — only to
an xbind that lists it, since bodies are decoded strictly. Every `can` and
`allowed` entry is a `{ok, why?, kind?}` judged by the same policy that
judges the request (kind `authority`, `policy` or `state`). While paused,
`workTree` `{changed, since}` counts the files that differ from the
checkpoint live reload was paused at. For the write audience `workTree`
also carries `branch`, on every read: the branch the tile's own repository
has checked out, `""` for a detached HEAD or none (*Assigned branches*).
A deployment with an assigned branch carries `branch`, and
`branchOverride` while it takes another this time; `can.branch` judges the
branch route. `caps` counts the tile's and the
workspace's non-primary deployments against their caps (*Runtime* below);
`edges` lists the tile's outbound edges and their policies (*The edge
policy*). Each deployment carries `url` and `api` (the qualified forms below;
the primary's are the bare ones), `origin` (origins mode), `data`, `vault`
(`{keys, placeholders}`), `limits` (`{memMiB, pids, diskGiB, overrides}`),
`deliveries` (true for the primary, and for any other deployment unless a
tile manager switched it off), `alwaysOn`, `alwaysOnDeclared`, `backup` (a
non-primary deployment's schedule), `registrations` (its cron jobs, bus
subscriptions, interface instances and ingress hosts, each with `dormant`)
and `wouldNotify` (its last 20 held notifications).

**Tile refs in a query string.** A query string never carries the
qualifier: a `+` there decodes to a space (D127j). A query parameter that
names a tile (`tile=`, `component=`) takes the tile's path, and the
deployment rides its own parameter, `deployment=` —
`/deployments?tile=apps/crm&deployment=dev`, never `?tile=apps/crm+dev`.
- The GET rows above, `/frame-token`, `/logs` and `/tile-status` answer 400
  `a deployment is named with deployment=, not tile+name (a '+' in a query
  string reads as a space)` for a tile parameter that holds a `+` and names
  no tile, and for one whose `+` a client sent unescaped: a space splitting
  it into a tile and a deployment name.
- A tile whose own name holds `+` (one created before the name rule) is an
  exact match and resolves: send its `+` as `%2B`, as `URLSearchParams` and
  Go's `url.Values` do.
- JSON bodies (`tile` of the POST rows) and URL paths (`/c/`, `/api/`) keep
  the `<tile>+<name>` ref: a path keeps its `+`.

`limit` above 200 and `wait` above 25 are clamped, not refused.

**Tile names never hold `+`.** Creating a tile whose path holds `+` in any
segment is refused (403) for every creator, admins included, on every
creation route (`/create`, `/clone`, `/templates/new`, `/builtins/import`,
`/git/import`) and by `bx new`'s local write: `<tile>+<name>` is a
deployment's URL. A directory named so before the rule keeps resolving, as
an exact match, but can't get deployments (`/deployments/add` answers 409
`<tile>'s name holds '+', which names a tile deployment in URLs: it can't get
deployments — …`); `bx doctor` flags it.

**Deployment URLs.** `/c/<tile>+<name>/…` and `/api/<tile>+<name>/…` reach
deployment `<name>`; the qualifier sits in the tile path's last segment.
- It is read only for a tile that has deployments, and only when nothing
  answers the path today: a tile, or anything on disk, at `<tile>+<name>`
  wins, so a directory whose name holds `+` keeps working, and for a tile
  without deployments `+<name>` means nothing. A `+` in a query string is
  never the qualifier (above).
- `<tile>+<primary>` is the bare URL, for people and the tile's own
  credentials. An unknown name answers 404 `<tile> has no deployment
  "<name>"` to those who may open deployment URLs, and 403 to everyone else.
- A qualified URL never enters a nested tile: 404 `<path> is a tile of its
  own; its deployments are /c/<path>+<name>/`. A nested tile's deployments
  carry the qualifier on its own last segment (`/c/apps/crm/widgets+dev/`).
- **Who.** People with write on the tile (terminal level and admins
  included), checked on every request: a reader gets 403 `deployment URLs
  need write access on <tile>`, whether or not the name exists. Also the
  tile's own frame, terminal and backend credentials bound to that
  deployment. Other tiles never, the alias included: 403 `<caller> opens <tile> by its bare
  URL, /c/<tile>/, which serves its primary: deployment URLs are for the tile
  itself and the people who work on it` (on `/api/`: `<caller> calls <tile>
  by its bare URL, /api/<tile>/, which reaches its primary: …`). The bare
  `/c/<tile>/` serves the primary whoever asks; only `/api/` has the
  self-call rule below.
- **Asset modes.** Legacy mode serves a deployment's non-document files by
  the credential-less subresource rule, as it serves the tile's. Tokens mode
  serves them under an asset token whose user writes the tile. Origins mode
  serves each deployment on its own origin (§Core, *tile origin*).
- **Documents.** A document at a deployment URL carries `<meta
  name="xbin-deployment" content="<name>">` and an import-map entry mapping
  `/c/<tile>/` to `/c/<tile>+<name>/` (under the asset token in tokens mode),
  so absolute self-imports stay in the deployment. An absolute `/c/<tile>/`
  in `src` or `href` loads the primary's files; relative URLs are the
  portable form. `xbin-component` stays the tile path. Such a document is
  never chrome, whatever its code declares. `?native=1` there runs that
  deployment's native entry.
- **Frame tokens.** A document's frame token names its deployment (six
  fields when it isn't `main`, §Authentication). A person gets one only for a
  deployment they may open (read for the primary, write for any other), a
  tile's own credential only for the deployment it is bound to, and the
  one-tree navigation rule holds only between primaries: a frame of `main`
  that fetches `dev`'s URL gets no `dev` token, and a frame of `dev` gets no
  primary token from the bare URL.
- **`/api/`.** Admins and the tile's own credentials bound to that
  deployment reach it; a person with write passes the URL gate and then
  holds no role there, as on the bare URL. The qualifier is consumed: the
  backend sees `/<endpoint>`. Lifecycle is the tile's: 409 with
  `X-XBin-Lifecycle`, as on the bare URL.
- **The inbound surface follows the primary's code.** `template`, `exposes`,
  `expose.roles`, `provides` and `chrome` come from the primary's code (its
  checkpoint while pinned): the bare `/api/<tile>/` template answer, whether
  bare documents are chrome, ingress routes, interface bindings and the roles
  callers are checked against. A save while live reload is paused, or
  attached to another deployment, changes none of them.

**Which deployment a call acts on.**
- **Bound credentials.** A tile's own credential acts in one deployment: an
  instance token in the one its backend generation runs; a frame token or
  tile-origin cookie in its document's (no field means `main`); a terminal or
  agent token in its session's target, or the current primary for a session
  that follows it. While the primary is protected a session that follows it
  is bound to nothing: every self-call and self-scoped route answers 403
  `the primary of <tile> is protected: terminal and agent sessions can't
  target it` until the session restarts. People, the owner, `xbin/cron` and
  `xbin/bus` are bound to nothing.
- **Self-calls.** `/api/<self>/…` reaches the bound deployment, at `admin`.
  The self-scoped routes act on it too: vault, kv, blob, bus publish, cron,
  bus subscriptions, interface instances, ingress hosts, tile-report,
  notify, prefs, and `/logs` and `/tile-status` without `deployment`.
  Naming another deployment of its own tile is refused: on `/api/` 403
  `<tile>'s deployment "<d>" can only call itself: this credential belongs to
  "<d>", not "<other>". A tile's code never reaches another deployment of its
  own tile; a person switches deployments by URL, a terminal by changing its
  target.`; on a self-scoped route 403 `a tile's own credentials act only on
  their own deployment (<bound>)`. A frame of a non-primary deployment whose
  user no longer writes the tile gets 403 `deployments of <tile> need write
  access`. A credential whose deployment was removed gets 404 `<tile> has no
  deployment "<name>"`; nothing ever falls back to the primary.
- **Named calls.** A call that names a tile acts on its primary unless it
  names a deployment, by a qualified URL or a `deployment` parameter. Existing
  routes keep taking the bare path in `component=`, `tile=` and `cwd=` (a
  qualified string there is looked up literally) and gain a separate
  `deployment` parameter. Naming a non-primary deployment needs write on the
  tile, or a credential bound to it; the tile's terminal and agent tokens
  may name any deployment in xbind's own reads (`/logs`, `/tile-status`,
  `/deployments`). Other tiles never reach a non-primary deployment.
- **The echo rule.** Every `deployment` parameter sent to an existing route is
  echoed in the answer: a `deployment` field, or `X-XBin-Deployment` on a
  non-JSON answer. An older xbind ignores the parameter and answers for the
  primary, so a client that finds no echo treats the answer as the
  primary's and says so.
- **Route classes.** Non-primary credentials are default-deny on
  `/api/xbin/*`. Every route is classed deployment-scoped (it acts on the
  caller's own deployment: the self-scoped routes, this family, the reads
  above), primary-only, or neutral (it has no deployment in it and behaves as
  before). On a primary-only route a non-primary credential gets 403 `this
  route is the primary's alone: a non-primary deployment's credentials can't
  use it (<deployment>)`, on a route without a class 403 `this route isn't
  available to a non-primary deployment's credentials yet (<deployment>)`,
  reads included; a credential bound to a removed deployment gets the 404
  above, and a session following a protected primary that primary's 403.
  Primary-only: tile creation (create, clone, git import, templates, builtin
  import and update), lifecycle, grant and binding writes, owner transfer and
  its impact report, access, users, orgs, sets, policy, defaults, screen
  writes, and every admin API (backups, the vault barrier, vaults,
  resources, auth-overview, backends, runtime, ingress, gpus, the VM policy,
  token rotation, view-as, the native-runtime, chrome, branding and
  workspace-policies writes, push config and devices), and partition mode
  decisions (`POST /partitions/mode`). Deciding a PR (`POST /code/pr/state`) is
  primary-only for backends: 403 `deciding a PR is the primary's act: a
  non-primary deployment's backend can't do it (<deployment>)`.
- **Audit.** A tile credential acting in a deployment other than `main`
  adds `deployment=<name>` to its `audit` line.

**`X-XBin-Deployment` and `XBIN_DEPLOYMENT`** follow one rule: absent means
the primary.
- `X-XBin-Deployment: <name>` is injected on every proxied request whose
  caller is one of a tile's own credentials bound to a non-primary
  deployment: its self-calls and its calls to other tiles' primaries. It is
  absent on the primary's calls and on those of people, the owner,
  `xbin/cron`, `xbin/bus` and ingress, and stripped inbound like every
  `X-XBin-*`, so receiving it means xbind set it. `X-XBin-From` stays the
  tile path. A proxied response from a non-primary deployment carries it
  too, set by xbind over the backend's own, so `curl -i` tells which
  deployment answered.
- `XBIN_DEPLOYMENT=<name>` is set for a backend whose deployment isn't its
  tile's primary when it spawns, and for a terminal or agent session whose
  target is a named deployment other than the primary at session start;
  absent otherwise. A reassignment of the primary restarts both backends
  (the new primary first) and the sessions named after either, so it holds
  for a process's life. It is informational: routing reads the credential,
  never env. `XBIN_COMPONENT` (the SDK's `Self()`, `xbin.self`) stays the
  tile path in every deployment, so vault URLs and `res:${self}` ids keep
  working, and `XBIN_RES_*` values are the same in every deployment.
- Credentials and stored state say `main` by leaving it out instead: the
  frame token's sixth field, tile-origin labels, tickets and cookies,
  `/sandboxes` ids, `deployment` on `bus` events and every storage key name a
  deployment only when it isn't `main`, so they keep their meaning when the
  primary moves.

**Registrations of a deployment that isn't the primary** (`main` beside
another primary included) are stored for that deployment.
- Its cron jobs and bus push subscriptions are active: a tick or delivery
  reaches the deployment that registered it (its backend, its data), never
  the primary. A tile manager's deliveries switch (on by default) turns them
  off: they stay registered and dormant — a dormant job doesn't tick, a
  dormant subscription receives nothing (its events count as
  `dormantEvents`) — and registering answers 200 with `"dormant": true`.
  `run-now` delivers one job once, whatever the switch says.
- Its interface instances and ingress hosts are dormant, deliveries on or
  not: registering answers 200 with `"dormant": true`, so a backend that
  registers at start keeps working, and they never route: no grants event,
  no re-wiring, no reconcile, and ingress hosts are zone-validated but meet
  no conflict check. Only the primary's route, and a reassignment changes
  which set does at once.
- A registration on another scope's resource is refused while its edge is
  `block`, and stored under `read`: a job only ever calls its own
  deployment's handler, and a subscription reads the scope's primary's bus,
  like a read binding, its edge re-checked at every delivery. Publishing is
  unchanged: into the deployment's own data only.
- `main`'s registrations stay in the stores they always used, whether or not
  `main` is the primary; another deployment's live in files of its own
  (§Filesystem contract), which an older xbind never loads.
- A non-primary deployment's tile status and notices travel only in
  `deployments` events (ops `status` and `notify`): nothing is pushed or
  counted, no `status` event is sent and `GET /tile-report` never lists them.

**Data per deployment.** Each deployment beyond `main` has its own data in
its tile's scope: kv, blob, bus and filesystem and sqlite volumes, declared
by that deployment's own code (its checkpoint's `scope.json` while pinned,
the work tree's while live reload follows it); beyond `main` a deployment's
data holds at most 64 resources. Its data is shared by the scope's tiles
that have a deployment of the same name.
- Resource ids and `XBIN_RES_*` values are the same in every deployment, and
  file resources are bound at the same paths, so code that stores absolute
  paths keeps working. kv, blob and bus publish calls resolve in the caller's
  deployment's data, with no wire change; volumes beyond `main` mount on
  first use. A start whose code declares a file resource its deployment has
  no data for fails with `no data namespace for <deployment>'s resource
  XBIN_RES_<NAME>`.
- Other scopes' data, and the workspace's, are read-only to non-primary
  deployments: 403 `<tile>+<name> may not write <res>: non-primary
  deployments reach other scopes read-only (edge policy "read")`.
- `Deployment.data.state` is `original` (`main`, untouched), `empty`,
  `seeded`, `restored` or `partial`, with `busy` (`seeding`, `resetting`,
  `restoring`) while an act runs. Meanwhile that data's kv, blob and bus
  requests answer 503 with `Retry-After`, the deployments using it don't
  start, and another data act answers 409 `<name>'s data is being
  <seeded|reset|restored>`. An act cut short (a failure, a crash) leaves the
  data `partial`, and its deployments refuse to start (`<act> of <scope> for
  <name> failed at <step>: reset or seed again`) until a reset or a new seed.
  Completion is a `deployments` event, op `data`.
- **Seed** copies the scope primary's data over the deployment's. Online by
  default: kv consistent at one moment, each SQLite database at one point in
  time, files and blobs file by file; `stop: true` stops the primary for a
  point-in-time copy of everything, and is required for a single-tenant
  container store and for a VM primary with file resources. Resources both
  deployments' code declare with the same type are copied; those only the
  target declares start empty; the rest are skipped and listed. Special
  files (FIFOs, sockets) are not copied, nor are the vault (vault copy is its
  own act), cron jobs, bus subscriptions or registrations. The seed is
  refused (409) while another data act runs, when the primary's data is over
  the target's disk limit or the disk is low, and (503) while the vault is
  sealed or encryption isn't ready.
- **Reset** empties the deployment's data. It never touches data some tile
  serves as its primary, nor `res:workspace/*`.
- **Deletion.** Removing a deployment deletes its data only with the scope's
  last tile that has that deployment. Data no tile claims any more (a
  `scope.json` that went away, a record removed) is kept 14 days, listed to
  admins, then deleted; `main`'s data is never collected.
- **Disk.** Each deployment's data beyond `main` is a quota bucket of its own,
  at the scope quota or a lower `diskGiB` limit. Non-primary data is
  write-blocked first when the disk is low (507). A tile's checkpoints, view
  repository, materialized trees and build artifacts count against a 10 GiB
  per-tile quota: an alert from 90%; when full, new captures and
  materializations answer 507, and running deployments keep serving.
- **Backups** of one deployment's data are the four routes above, beside
  `POST /backup`, whose body is unchanged. A deployment archive is refused
  by older xbinds.

**The edge policy.** Every outbound edge of the tile — an interface binding
(`slot:<interface>`, the net slot included) or a grant (`grant:<target>`:
a call, resource, code, `gpu:` or `cap:` grant) — has a policy for the
tile's non-primary deployments; the primary uses every edge as before.
- `read`, the default where the role can be read-clamped: the call reaches
  the provider's primary with `X-XBin-Role` clamped to `reader` (a bus
  `publisher` to `subscriber`). Providers treat `reader` as read-only.
- `inherit`, for the net slot and capability grants: the tile's own
  authority, never host networking or a provider splice. The net slot
  inherits the tile's relay policy; a tile whose network shares the host's
  gives its non-primary deployments no egress, whatever is stored (`effective`
  `block`, with `why`). `gpu:` grants default to `block`, other capability
  grants to `inherit`.
- `block`: the call is refused, naming the deployment, the edge, its target
  and the policy: `<tile>'s non-primary deployment "<d>" may not use edge
  <id>: the tile's edge policy for it is "block". A tile manager can change
  it in the Deployments panel.`
- Some edges take `block` alone, with no override: a custom role whose
  `implies` don't reach `reader`, stream and lan-ingress bindings, a net slot
  bound to a provider tile, and a workspace-level filesystem or sqlite
  resource on a workspace-scope tile. Their refusal says so (`… can't use
  edge <id>: <why>, so non-primary deployments are blocked from it …`). A
  provider makes a custom role clampable by declaring `implies: {"<role>":
  ["reader"]}`. Two consequences: llm-gw guards completions with `writer`,
  so an LLM-using tile's non-primary deployments can list models but not run
  a turn; and the agent template reaches its sandbox managers through the
  custom role `consumer`, so its non-primary deployments are blocked from
  them.
- A stored value this xbind doesn't know, or one no longer valid for the
  edge, reads as `block`, and when several edges authorize one call any
  `block` among them refuses it. `Edge.refused` and `Edge.clamped` count
  since xbind started.
- Governance grants are no edge: approving `xbin` or an `xbin:*` target for
  a tile that has non-primary deployments answers 409, and a tile holding one
  can't have them.

**Runtime.**
- A backend of a non-primary deployment needs `--isolate`, as a pinned one
  does: without it, adding one answers 409, and a start fails with `<tile>:
  deployment <name> runs only in a sandbox (--isolate), and this xbind runs
  backends without one`.
- A workspace runs at most 3 non-primary deployments per tile, 24 per
  workspace and 12 non-primary backends at once. A start past them is
  refused, and the refusal shows in the deployment's build status and in
  `/sandboxes` failures. At most max(1, NumCPU/4) builds for non-primary
  deployments run at once across the workspace; a primary's build never
  waits.
- Host networking and net-provider splices serve a tile's primary only: a
  non-primary deployment of such a tile starts with no egress, and its
  backend log says why. It gets no stream interface dial (each fails: with
  relay egress its relay refuses the dial and writes a line to its backend
  log; without egress its sandbox has no route to the interface's
  address), no ingress, no lan-ingress legs and no provider roster.
- Its VM guest is charged to the tile, admitted only while the primary's next
  guest still fits the workspace's VM room, and stopped to admit the
  primary's.
- A tile keeps one cgroup leaf, `comp-<key>`, while it runs only `main`;
  once it runs another deployment its generations move under a per-tile
  parent, `tile-<key>/d-<name>/backend`, whose tile node weighs one flat
  leaf and whose deployments' leaves carry their own memory and pids caps.
  Limit alerts name the deployment whose leaf hit its cap.
- It is kept up only while its own code declares `alwaysOn` and its alwaysOn
  switch is on; otherwise it starts on its first request and is reaped when
  idle, like any lazy backend.
- Its backend log, setup output included, is
  `.xbin/deploy/<tile-key>/d/<name>/backend.log`, read with `GET
  /logs?deployment=`; `main`'s stays `.xbin/log/<key>.log`.

**The operations** beyond what the rows say:
- **`live-reload/pause`** pins the live reload target to a fresh checkpoint of
  the work tree at request time; a backend's swap onto it follows as a
  queued deploy (`how: "pause"`), normally of identical code; for a static
  tile the commit is the whole deploy. A failed build or start leaves live
  reload paused and the deployment pinned to the attempted checkpoint (its
  state `failed`): every restart runs it, and Reload now retries it. Pinning a
  `go`, `node` or `python` tile without `--isolate` answers 409 (kind
  `policy`); static tiles pause everywhere; resuming never needs isolation.
- **`live-reload/now`** answers `unchanged: true` when the work tree equals
  the deployment's checkpoint and nothing is queued; `expect` must be a
  prefix of the fresh capture, else 409 `the code changed since you reviewed
  <expect> (now <actual>)`.
- **`live-reload/resume`** onto `main` with nothing else set removes the
  record, the deploy journal and the view repository (`record: false`); the
  checkpoint store and its deploy log stay, unread, until the next opt-in,
  whose deploy ids keep counting.
- **`deploy`** without `deployment` names the primary; `checkpoint` and
  `expect` together are 400; `restart: true` starts a new generation of the
  current code, clears the crash breaker and moves nothing (with
  `checkpoint` or `expect`: 400 `restart runs <name>'s current code: send no
  checkpoint or expect with it`). A checkpoint the deployment already runs
  answers `unchanged: true` while its backend is healthy or being built; when
  the backend is crash-looping, failed or not running, or its last move
  failed, the deploy starts a new generation from the kept build (`how:
  "restart"`) and clears the crash breaker. The same holds for `rollback`
  naming the running checkpoint; `live-reload/now` of an identical work tree
  always answers `unchanged: true`.
- **`rollback`** without `checkpoint` takes the newest `ok` entry whose
  checkpoint differs from the current one, else 409 `<name> has no earlier
  checkpoint in its deploy log`.
- **Queueing.** One deploy in flight per deployment and up to eight waiting;
  a request whose checkpoint equals the tail of the queue is merged into it
  (the answer carries that entry); a ninth answers 409 `<name> already has 8
  deploys waiting; try again when one finishes`. Removing a deployment or
  disabling the tile ends its queued and running deploys `cancelled`.
- **The journal.** Every accepted attempt is journaled until its deploy-log
  entry is written; after a crash, the next boot logs it: queued as
  `cancelled`, swapped as `ok`, running as `failed` with `interrupted: xbind
  restarted mid-deploy; <name> runs <code>`.
- **Checkpoints** are refused past their caps (409, `checkpoint of <tile>
  refused: …`, naming the largest paths) and rate-limited per tile (429,
  `<tile> was checkpointed too often; retry in <n>s`). `by` is `user:<id>` or
  `owner`; `via` is the acting credential's kind. After each successful
  deploy xbind collects the tile's checkpoint store, keeping every
  deployment's current checkpoint, those of each deployment's last 20
  successful deploys and anything younger than 24 hours; `purge` removes one
  at once. Checkpoints capture gitignored files such as `.env`: purge one
  that caught a secret, and keep secrets in the vault.
- **Assigned branches** (`branches/1`, D131). A deployment other than `main`
  and the primary may require a branch of the tile's own repository: a
  requirement and a label, not a feed. The work tree feeds it only while
  it has that branch checked out, read from `.git/HEAD` beneath the tile
  (no git runs); a detached HEAD, no repository or one xbind can't read is
  no branch.
  - **The ops that feed it from the work tree** — `live-reload/attach` and
    `live-reload/resume` onto it, `live-reload/now` while it is where live
    reload last was, a `deploy` of the work tree onto it, and `add` from the
    work tree (with `attach` or not) — answer 409 `<name> is assigned branch
    <b>, and the work tree is on <w>: check out <b>, or send
    confirm:"other-branch" to use <w> this time`, dry runs included, whose
    `impact.branch` is `{deployment, assigned, workTree, other?}`.
    `confirm: "other-branch"` takes the work tree's branch this time; attach
    and resume keep it as the deployment's `branchOverride` (which the other
    ops on it take too), lapsing when live reload moves or the work tree's
    branch changes again. A work
    tree on no branch can't be followed even so (409 `… isn't on a branch
    …`). Their capture reads the branch again: one taken while a checkout
    moved HEAD answers 409 `the work tree's branch moved while xbind
    captured it for <name> (<b>, now <w>): a checkout raced this request,
    and nothing shipped; …`. A deploy of a checkpoint, promote and roll back
    aren't fed by the work tree and aren't asked.
  - **Saves.** While live reload follows it, a save reaches it only once
    xbind found the work tree on its branch (or its override) and captured
    it, with the branch read again at the capture. A save on another branch
    — or one a checkout raced — deploys nothing: live reload pauses, the
    deployment pinned to the code it runs (the last save deployed on its
    branch; its deploy log entry is a `pause` by `xbind`), and op `branch`
    tells the write audience (§`/ws/events`). A pause on another branch pins
    it the same way, never to the other branch's work tree. Tiles without
    an assigned branch save as before.
  - Checkpoints taken from the work tree carry an `Xbin-Work-Tree-Branch`
    trailer (in the git view too). A deploy entry's `branch` is the branch
    its own capture was taken on (kept in the deploy log as an
    `Xbin-Branch` trailer, which an older xbind leaves unread), else the
    branch its checkpoint was first captured on.
  - **newBranch** runs a confined `git switch --create=<b> --end-of-options`
    in the tile: it creates and checks out a new branch at the work tree's
    HEAD and changes no file, so nothing reloads. It never switches to an
    existing branch; an add refused after it keeps the branch.
  - Reassigning the primary to a deployment clears its branch. An older
    xbind keeps `branch` and `branchOverride` in the record as it found
    them, and ignores them.
- **A protected primary.** Only tile managers change its code, each naming
  the checkpoint they reviewed; the server commits only while `seq` is
  unchanged and re-checks the actor's authority at commit. Terminal and
  agent sessions never target it, and its vault is written only by its own
  backend and by tile managers in their own session.

**A deploy of a checkpoint** reports on the `deployments` event, never
`build-*`: a failed one paints no overlay, the previous generation keeps
serving, and the compiler output goes to the deployment's log under `---
deploy of c:<id> failed <time> ---`. After a swap that changed the code a
deployment serves (reload now, deploy, promote, roll back, resume, attach),
the primary gets one bare `reload` and a non-primary deployment op `reload`;
pausing and restarts reload nothing. A crash-looping pinned backend says
`deploy a fixed checkpoint or restart it`: a save doesn't reach it. A pinned
backend needs isolation: on an xbind restarted without
`--isolate` it is held (its `/api/` answers `pinning a backend to a
checkpoint needs isolation (--isolate)`, the pages keep serving the
checkpoint; a restart that goes through a build fails it with a longer text
that also names `--isolate`), and it never runs the work tree instead.

**Texts** beyond the refusals the routes above name:

| Status | `error` |
|---|---|
| 400 | `need ?tile= (a tile's path: apps/crm; name a deployment with deployment=)` |
| 400 | `a deployment is named with deployment=, not tile+name (a '+' in a query string reads as a space)` |
| 400 | `the tile ref names deployment "<a>" and the body "<b>": send one` |
| 400 | `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters` |
| 400 | `<consequence>: send confirm:"<token>" to proceed` |
| 400 | `the primary of <tile> is protected: name the checkpoint you reviewed (send <checkpoint\|expect> and seq)` |
| 400 | `id and before are deploy ids; wait, seconds (at most 25); limit, 1 to 200` |
| 400 | `bad spec "<spec>": c:<id> \| deployment:<name> \| work-tree` |
| 400 | `path is one clean tile-relative file; stat is 1 or absent` |
| 403 | `deployments of <tile> need read access` (the state, outside the reader audience) |
| 403 | `deployments of <tile> need write access` (the log, the diff and the checkpoint remote, outside the write audience) |
| 403 | `deployment URLs need write access on <tile>` (the state naming a non-primary deployment with `deployment=`, outside its audience, whether or not it exists) |
| 403 | `a tile's own credentials act only on their own deployment (<bound>)` (the log, from a non-primary deployment's credentials naming another) |
| 403 | `<act> needs terminal-level access on <tile>` (`<act>`: "pausing live reload", "deploying", …); for any tile credential but the tile's own terminal and agent sessions (a frame, a backend, another tile's) it adds ` — only people and the tile's own terminal and agent sessions operate its deployments` |
| 403 | `<act> is a tile manager's act: the tile's owner, its org's admins, or a workspace admin` |
| 403 | `<act> is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it` |
| 403 | `<act> is a workspace admin's act, done in a person's own session` |
| 403 | `the primary of <tile> (<name>) is protected: only tile managers change its code, and not from a terminal or agent session` |
| 403 | `<scope>'s "<name>" data was <seeded\|restored> by <by> <when>: joining it is a tile manager's act` |
| 404 | `no such tile: <tile>` · `<tile> has no deploy <id>` · `<tile> has no deployment "<name>"` · `<tile> has no checkpoint <id>` |
| 409 | `<tile> has no deployments yet: pause live reload or add a deployment first` — a tile without a record, for any operation but an opt-in and purge, and for the log; the checkpoint remote answers it as a 404 |
| 409 | `<tile> has no deployments: its work tree is what runs, so there is nothing to diff` |
| 409 | `the deployments of <tile> changed (seq <n>); reload and retry` |
| 409 | `live reload is attached to <name>; <act> works while it is paused` |
| 409 | `<name> is assigned branch <b>, and the work tree is on <w>: check out <b>, or send confirm:"other-branch" to use <w> this time` (branches/1) |
| 409 | `the work tree's branch moved while xbind captured it for <name> (<b>, now <w>): a checkout raced this request, and nothing shipped; check the branch and retry` |
| 409 | `checkpoint id <id> is ambiguous in <tile>; use more digits` |
| 409 | `pinning a backend to a checkpoint needs isolation (--isolate)` |
| 409 | `<tile>'s deployment record was written by a newer xbind (schema <n>)` — the tile is held: its backends don't start, `/c/` and `/api/` answer 503 with this text, and every write here answers 409 |
| 409 | `<act> is turned off on this xbind (--tile-deployments=off): it would create or extend deployment state; resuming live reload onto main, removing a deployment, resetting its data and unprotecting still work` |
| 429 | `<tile> already has a diff running and one waiting; retry shortly` |
| 504 | `the diff of <tile> took longer than 30 s (narrow it with path or stat)` |

A view-as session's POSTs are refused 403 read-only by the usual
middleware.

**The deploy log** holds every finished attempt: one confined commit per
attempt on the deployment's chain in the checkpoint store. An attempt that
made no checkpoint names the empty tree, and so does an entry whose
checkpoint was purged; `error` keeps the failure's first line, at most 500
bytes (the whole text is in the deployment's log). Reading it needs write on
the tile; a non-primary deployment's own credentials read its entries only.

**The diff** runs confined (one per tile at a time with one waiting, two
across xbind). `path` is literal, not a pattern. A `work-tree` side is a
capture and spends the capture rate; a deployment that follows the work tree
means `work-tree`. The `stat=1` answer carries the `X-XBin-Checkpoint-From`
and `-To` headers too. `workTree.changed` and the diff count added, changed
and removed files, never a nested component's.

**The checkpoint remote** (`GET /checkpoints/<tile>.git/<path>`) serves the
tile's view repository by dumb HTTP: `HEAD`, `info/refs`,
`objects/info/packs`, `objects/pack/pack-<hex>.pack|.idx` and loose objects,
with git's content types; GET and HEAD only; everything else 404, and
`?service=` is ignored, so git uses the dumb protocol. `deploy/<name>`
exists only while `<name>` is pinned, and `HEAD` names `deploy/<primary>` —
dangling while the primary follows the work tree. View commits are
parentless and carry `Xbin-Checkpoint` and `Xbin-Work-Tree-Head` trailers
(the latter the base for `git rebase --onto`). A fetch racing a refresh can
fail once and succeeds on retry. Terminal and agent sessions opened while
the tile has a record carry it as the `xbin-deploy` remote, in two more
`GIT_CONFIG_*` pairs (`GIT_CONFIG_COUNT` 4); sessions without a record keep
the two they always had.

### Tile sandboxes (manager tiles)

A **manager tile** runs coding sandboxes for the tiles it serves (the
sandbox-manager contract, [sandbox-manager.md](sandbox-manager.md)). On
xbin its backend defines and drives them through the routes below (D120),
which mirror the contract, so the manager forwards most calls unchanged.
The Go SDK wraps them: `xbin.SandboxAPI()` ([sdk.md](sdk.md) §Tile
sandboxes).

- **Who.** Only a *manager call* reaches them: the tile's **backend** — its
  instance token, over the gateway — holding **`cap:sandboxes`**, a grant
  only a workspace admin approves. The key is the caller's own tile: a
  manager names only its own sandboxes. Everyone else gets 403
  `not-allowed`: a frame of the same tile, terminals, cron and signed-in
  people. An **admin** may read and set the policy and list, stop and
  delete a tile's sandboxes (`?tile=`) — and never defines one, runs a
  command in one, reads its files or attaches to it.
- **Deployments.** A manager tile's sandboxes are its `main`
  deployment's. The backend of any other deployment of it (§Tile
  deployments) gets 501 `unsupported` on every manager route — never
  `main`'s sandboxes, files or quota — and so does an admin's `?deployment=`
  naming one; a deployment's own sandbox set isn't built yet.
- **Isolation.** Without `--isolate` every manager route but `runtime`
  answers 501 `unsupported` ("tile sandboxes need isolation"), and `runtime`
  says `isolation: false`.
- **In this xbind** the definition routes work — runtime, policy, list,
  create, get, patch, delete — and so do start, stop, reset and rebase, in
  both modes, the commands (`run`, execs and their output, stdin, signals,
  resizes and the TTY WebSocket), the file, tar and copy routes, and
  snapshots, restores and clones, and the ports proxy; `runtime.caps` lists
  the contract capabilities served (`exec`, `tty`, `files`, `tar`,
  `snapshots`, `clone`, and `ports` since D135 — an older xbind leaves it
  out, and its ports route is a 404).
- **Ports** (D135). `ANY /sandboxes/<name>/ports/<port>/<path>` proxies
  the request — any method, a WebSocket upgrade included — to a server
  listening on TCP `<port>` (1–65535) on the sandbox's **own loopback**:
  xbind asks the sandbox's agent for a connection, and the agent dials
  `127.0.0.1:<port>` (else `[::1]`, for a server that bound `localhost`)
  inside the sandbox and splices it; each request is a connection of its
  own. It is inbound only: nothing in the sandbox reaches xbind or the
  workspace through it. `<path>` and the query go on as they came (escaped;
  a segment that decodes to `.` or `..` is 400), `Host` is
  `localhost:<port>`. `Authorization`, `Cookie`, `Sbx-User`, `X-XBin-*`,
  `Forwarded` and `X-Forwarded-*` never reach the server, and its
  `Set-Cookie` and `X-XBin-*` never come back. The sandbox must be running:
  the route never starts one (409 `state`: its server would be gone
  anyway); nothing accepting on the port is **502 `not-listening`**; an
  agent from before ports (restart the sandbox) is 501 `unsupported`. A
  request in flight — a WebSocket for as long as it is open — holds off the
  idle stop.

```
GET    /sandboxes/runtime          manager. what this tile may use now → {enabled,
                                   isolation, modes:[{mode,accel?}],
                                   unavailable:[{mode,reason}], users, egress:
                                   [{class,slot?,ref?,reach,rules?,note?}], caps,
                                   limits:{sandboxes,running,memMiB,vcpus,diskGiB,
                                   perSandbox:{memMiB,vcpus,diskGiB,maxMemMiB,
                                   maxVCPUs,maxDiskGiB,pids},idleStopMin,
                                   runTimeoutMaxMs,runOutputMax,execsRunning,
                                   outputRing,stdinMax,fileMax,tarMax,waitMaxSec,
                                   flows:{tcp,udp}},
                                   used:{sandboxes,running,memMiB,vcpus,diskBytes}}
GET    /sandboxes/policy           admin. → {policy (effective), stored (0 = default),
                                   error? (why the policy file can't be read)}
PUT    /sandboxes/policy           admin. a partial policy, merged onto the stored
                                   one → {policy, stored}; 400 invalid when out of range
GET    /sandboxes                  manager. → {sandboxes:[SandboxInfo]} (an admin's
                                   GET /sandboxes is the registry view above)
POST   /sandboxes                  manager. define one → 201 SandboxInfo (200 when
                                   clientId repeats the same request)
GET    /sandboxes/<name>           manager. → SandboxInfo
PATCH  /sandboxes/<name>           manager. change it → SandboxInfo (restartNeeded
                                   when a change waits for the next start)
DELETE /sandboxes/<name>[?tile=]   manager, admin (?tile=). stop it, put its
                                   state aside for a confined removal, forget
                                   it → 204 (the removal goes on after it)

POST   /sandboxes/<name>/start?wait=         manager. → SandboxInfo (running, or
                                             stopped with why in stateDetail)
POST   /sandboxes/<name>/stop?wait=[&tile=]  manager, admin. sync, then kill; state
                                             kept, running execs end killed
                                             → SandboxInfo
POST   /sandboxes/<name>/reset?wait=         manager. stop, wipe the state, pin the
                                             current base image, start again if it
                                             ran → SandboxInfo
POST   /sandboxes/<name>/rebase?wait=        manager. stop, keep the state, pin the
                                             current base image, start again if it
                                             ran → SandboxInfo

POST   /sandboxes/<name>/run                 manager. the contract's run → its result
GET    /sandboxes/<name>/execs               manager. → {execs:[Exec]}
POST   /sandboxes/<name>/execs               manager. → 201 Exec (200 on a clientId repeat)
GET    /sandboxes/<name>/execs/<id>          manager. → Exec
DELETE /sandboxes/<name>/execs/<id>          manager. kill the group, forget it → 204
GET    /sandboxes/<name>/execs/<id>/output?since=&max=&waitMs=&encoding=text|base64
                                             manager. → the contract's chunk
POST   /sandboxes/<name>/execs/<id>/stdin?eof=   manager. raw body → 204 (eof=1 closes stdin)
POST   /sandboxes/<name>/execs/<id>/signal   manager. {signal: INT|TERM|KILL|HUP,
                                             group?} → 204
POST   /sandboxes/<name>/execs/<id>/resize   manager. {rows, cols} → 204 (tty)
GET    /sandboxes/<name>/execs/<id>/tty?sessionId=&sandboxId=&forUser=
                                             manager. WebSocket: attach (below)
GET    /sandboxes/<name>/tty?cwd=&cmd=&rows=&cols=&uid=&gid=&forUser=&sessionId=&sandboxId=
                                             manager. WebSocket: start a tty exec
                                             (the login shell unless cmd) and attach

GET    /sandboxes/<name>/files/stat?path=
GET    /sandboxes/<name>/files/content?path=&offset=&length=      → bytes + ETag
PUT    /sandboxes/<name>/files/content?path=&mode=&mkdirs=1&ifMatch=&ifNoneMatch=*
                                             raw body → the stat
GET    /sandboxes/<name>/files/list?path=&limit=
POST   /sandboxes/<name>/files/mkdir         {path, parents} → 204
POST   /sandboxes/<name>/files/remove        {path, recursive} → 204
POST   /sandboxes/<name>/files/move          {from, to, overwrite} → 204
GET    /sandboxes/<name>/tar?path=&exclude=… → application/x-tar
PUT    /sandboxes/<name>/tar?path=&mkdirs=1  tar body → 204
POST   /sandboxes/copy                       {from:{sandbox, path}, to:{sandbox,
                                             path}, overwrite} → 204 (both the caller's)

ANY    /sandboxes/<name>/ports/<port>/<path>?<query>
                                             manager. an HTTP reverse proxy to a
                                             server on the sandbox's loopback,
                                             WebSocket upgrades too (below)

GET    /sandboxes/<name>/snapshots           manager. → {snapshots:[{id, name, created,
                                             bytes, pending?}]}
POST   /sandboxes/<name>/snapshots?wait=     manager. {name, clientId} → 201 the snapshot
                                             (200 on a clientId repeat; 202 {…, pending:
                                             true} when ?wait ran out first)
POST   /sandboxes/<name>/snapshots/<sid>/restore?wait=
                                             manager. → SandboxInfo (execs killed; busy
                                             when ?wait ran out first)
DELETE /sandboxes/<name>/snapshots/<sid>     manager. → 204
```

**Conventions.** Errors are the contract's: `{error, refusal, state?,
etag?, retryAfterMs?}` with its statuses (`invalid` 400, `not-allowed` 403,
`not-found` 404, `state`/`exists` 409, `lost` 410, `precondition` 412,
`too-large` 413, `limit` 429, `unsupported` 501, `unavailable` 503). Bodies
are JSON decoded leniently — unknown fields are ignored, so a newer SDK
works against an older xbind — and capped: 64 KiB for a definition, a PATCH
and the policy (413 `too-large` past it). Times are unix ms, sizes bytes
unless named. Names match `[a-z0-9][a-z0-9-]{0,31}`; `runtime`, `policy`
and `copy` are reserved. Exec ids match `[0-9a-f]{6}-[0-9]{1,12}` and
snapshot ids `s-[0-9]{1,12}`. A path with a `.` or `..` segment, or an
encoded `/`, `.` or `\` (`%2F`, `%2E`, `%5C`) in any segment, and a
`<name>`, `<id>` or `<sid>` that fails its grammar, are 400 `invalid`
before anything is looked up, so an id a manager forwards can never
address another route or sandbox. (xbind's router may answer first: a
plain dot segment with a redirect, an encoded one with 400 `invalid`, and
an encoded `/` that leaves no route for the method with 404 or 405.
None reaches a sandbox.) The
data plane — `run`, starting an exec, stdin, signals, resizes, file
writes, tar uploads and copies — isn't audit-logged;
definitions, lifecycle, snapshots and the policy are.

**A definition** (`POST /sandboxes`; `PATCH` takes the same fields but
`name`, `mode` and `from`, plus `version`):

```
{"name": "sb-7f3a", "mode": "namespace" | "vm",
 "memMiB": 2048, "vcpus": 2, "diskGiB": 20,
 "net": {"egress": "none" | "class:<slot>"},
 "mounts": [{"res": "res:apps/coding-sandbox/work", "path": "shared", "at": "/mnt/shared", "ro": false},
            {"source": true, "at": "/opt/manager"}],
 "defaults": {"cwd": "/work", "uid": 1000, "gid": 1000, "shell": "/bin/bash", "env": {"HOME": "/home/dev"}},
 "labels": {"manager.v": "1"}, "for": "apps/agent", "forUser": "alice",
 "idleStopMin": 30, "autoStart": true, "clientId": "c-5e1", "start": false,
 "from": {"sandbox": "img-base", "snapshot": "s-2"}}
```

- `mode` is required and must be available now (`runtime.modes`; `invalid`
  names why) — xbind never picks it, and a VM never falls back to a
  namespace.
- Sizes: 0 is the policy default; a size over a cap is clamped to it, and
  the answer says what applied. A VM's disk only grows (`PATCH` to less is
  `invalid`). More sandboxes than `perTile.max`, or VM disks summing over
  `perTile.diskGiB`, is 429 `limit`.
- `net.egress` is `none` (the default) or a **sandbox-net slot** the
  manager's manifest declares (`interfaces: {"internet": {"kind":
  "sandbox-net"}}`); `runtime.egress` lists them with what each reaches.
- A `res` mount is a `filesystem` resource of the manager's own scope that
  it holds the way its backend's `XBIN_RES_*` variables need: declared in
  its `uses` **and** granted (a same-scope `uses` entry is its own grant).
  A grant left over after the manifest dropped its `uses` entry mounts
  nothing ("declare it in uses"). A reader's mount is read-only. `path` is
  a clean relative sub-path of the resource. `source: true` mounts the
  tile's own code, read-only. `at` is absolute and clean, not `/`, and not
  under `/proc`, `/sys`, `/dev`, `/run/xbin`, `/opt/xbin` or `/.xbin-vm`
  (a VM's plumbing). `path` may name a file as well as a directory, in
  both modes. `sqlite` and
  other kinds are `invalid`.
- `defaults.env` keys `XBIN_*` are `invalid`: a sandbox never gets an xbin
  identity. `uid`/`gid` must be runnable (`users: root` — a namespace host
  mapping a single uid — allows only 0). `labels` are opaque, ≤ 1 KiB in
  all; `for` and `forUser` (≤ 128) are claims, stored and shown, widening
  nothing.
- `clientId` makes a create repeat-safe: the same request again answers 200
  with the sandbox, another request with that id is 409 `exists` (so is an
  existing name). `version` in a `PATCH` refuses a lost update (412
  `precondition`); a `PATCH` changes the fields it names, `defaults` and
  `labels` as a whole. `start: true` starts it after the create; a start
  that fails leaves it `stopped`, the failure in `stateDetail`. `from`
  clones (below).

**Running one.** A start runs the sandbox's first process — in
namespace mode, xbind's agent as PID 1 under the terminals' restricted
lockdown (no nested user or mount namespaces, mount points never
followed, `NO_NEW_PRIVS`), over its own upper pinned to the base image it
first started on, with its own hostname, its mounts and its egress class
resolved again — and answers once the sandbox is `running`. Its network
goes through a relay in xbind with no route to the host or to xbind (every
address the host delivers locally, and xbind's listen addresses, are
refused whatever the class says; a public address a NAT outside the host
maps to it isn't one — [isolation.md](isolation.md)); under `none` a connection is reset and a
DNS query answered REFUSED at once. Each sandbox may hold at most
`runtime.limits.flows` connections, and all tile sandboxes together a
share of xbind's descriptors. Where xbind's cgroup is delegated each
running sandbox has its own cgroup — `memory.max` its `memMiB` + 128 MiB
(its agent), no swap, `pids.max` the policy's `perSandbox.pids`, its
`vcpus` as a hard CPU cap — inside one `comp-tilesbx-*` cgroup capped by
the policy's `total`; its commands are what the OOM killer takes first, so
a command over the memory cap is killed and the sandbox runs on.

In **VM mode** the sandbox is a microVM with its own kernel, where it is
root (docker works), booted from the same base image. `runtime.modes`
offers it while the VM policy's `tiles` switch is on (`GET /vm`) and,
where VMs run emulated (`accel: "emulate"`, several times slower), its
`tilesEmulated` too; `runtime.unavailable` says why not. Its state is its
own disk (sparse, `diskGiB`; a `PATCH` that grows it applies at the next
start), its mounts are served into the guest at the same places, and its
network goes through the same relay. Its VM counts against the
workspace's VM count and memory budget and against `tilesBudgetMiB`: a
start past any of them is 429 `limit`. Where cgroups are delegated its
cgroup holds its `memMiB` plus what the VMM needs (192 MiB, 512 emulated),
512 processes and `vcpus` + 1 CPUs. A stop flushes the guest's disks
before the VM goes.

A start answers the sandbox as it stands: `running`, or `stopped` with
the failure in `stateDetail` (the sandbox's own start-up error, quoted). It
is refused 409 `state` when the sandbox is in `error`, or its start finds
it so (the sandbox reads `stopped` until a start looks) — its state went
missing, its base image is no longer installed, or its upper was written
by the other overlay flavour (`stateDetail` says which; a reset repairs
it, and a rebase repairs a missing base) — or while an earlier run's
processes still hold its state (a start waits up to 5 s for them first);
503 `unavailable` while the sandboxes policy is off or its file can't be
read, the workspace disk is low, the vault is sealed (a resource mount),
VM mode is unavailable (a VM sandbox: the reason said) or the tile is
disabled; 403 `not-allowed` when the tile no longer holds
`cap:sandboxes`; 400 `invalid` when a mount or the egress class is no
longer the tile's to use. A start of a running sandbox changes nothing.
One of the workspace's own reasons below that arrives while a sandbox is
still starting — a revoke, a disable, a seal, low disk, the policy
switched off — ends that start before it comes up: 503, `stopped`, the
reason in `stateDetail`; so does a mount the tile no longer holds as the
start resolved it (a `res:` grant revoked, a writer now a reader): 400
`invalid`.

**Admission.** Every start is booked against the tile's quotas — with it,
at most `perTile.running` sandboxes running, their `memMiB` summed within
`perTile.memMiB` and their `vcpus` within `perTile.vcpus` — and against the
workspace's `total.memMiB` (each running sandbox counts what its cgroup
may take: its `memMiB` + 128 MiB, a VM's `memMiB` + its VMM's 192 MiB, 512
emulated); over any of them is 429 `limit`,
naming the cap. So is a tile whose sandboxes' bytes on disk, snapshots
included, pass `perTile.diskGiB`; those bytes never count against the
tile's resource-write quota. The booking is atomic: ten starts at once
under `running: 4` run exactly four. A lowered cap applies at the next
start (a running sandbox shows `restartNeeded` for its sizes).

**Waiting.** Lifecycle calls take `?wait=<seconds>` (up to
`limits.waitMaxSec`; more is clamped; absent is `waitMaxSec`): the call
answers once its transition is done or the wait runs out, with the
sandbox as it stands (`starting`, say) — the transition goes on, and `GET`
says where it got. `?wait=0` answers without waiting. A refusal is
answered as itself. `POST /sandboxes` with `start: true` takes it too.

**Reset and rebase.** Both restart a sandbox that was running: it is
stopped (its execs end `killed`), reset or re-pinned, and started again,
so it is `running` afterwards; a stopped one stays stopped. Both keep its
snapshots and both answer the sandbox. **Reset** puts the state aside for
a confined removal and clears `base`, so the next start runs on the
current base image from an empty upper; it repairs `error` and repeating
it is harmless. **Rebase** keeps the state and pins it to the current
base image — what a package manager installed on the old one may break;
it repairs a missing base, but an upper of the other overlay flavour only
a reset repairs. `base.outdated` says a sandbox is pinned to an older base
than the current one.

**Idle stop.** A running sandbox that sees no activity for its
`idleStopMin` (its definition's, else the policy's) is stopped, state
kept, `stateDetail` "idle for N minutes: stopped, state kept
(idleStopMin)". Activity is a call on the sandbox (reading its
`SandboxInfo` or the list isn't one), exec input and output, a terminal
attach, detach or keystroke, and a file operation; `lastActive` is the
last, and stays so after the sandbox stopped. Work in flight holds it off
however long it runs: a non-tty exec, a `run`, a file, tar or copy
operation, an attached terminal client. A terminal exec nobody is
attached to and that prints nothing is idle.
A `PATCH` of `idleStopMin`, or a policy change, applies to running
sandboxes at once.

**Auto-start.** An exec or file call on a `stopped` sandbox with
`autoStart` starts it and waits for it (the command's `timeoutMs` starts
after); on a `starting` one it waits for `running`; on a `stopping` one it
waits for the stop, then starts it again — within `waitMaxSec`, else 409
`state`. Without `autoStart` a stopped or stopping sandbox answers 409
`state`.

However a run ends, the sandbox is `stopped` and `stateDetail` says why:
`""` after the manager's stop; "stopped by a workspace admin"; "the
sandbox's agent exited (code N)" or "(killed by SIGKILL)"; "the sandbox's
root filesystem (fuse-overlayfs) died"; "out of memory: N processes were
killed"; "the sandbox's agent stopped answering (its control connection
closed)"; "the VM exited: <the last lines of its console>" (a VM whose
VMM died); "an admin switched VM tile sandboxes off (vm policy:
tiles)…" (or `tilesEmulated`, for one running emulated); "its network
(class:<slot>) narrowed…" when the class's new
rules don't cover the running ones (a class that widens shows in
`egressNext` and applies at the next start, `restartNeeded`). A stop's
`stateDetail` is kept until the next start. xbind stops its tile sandboxes,
synced, when it exits, and they die with it when it dies: after a restart
every sandbox is `stopped`, its state kept, and an exec id of the old boot
answers 410 `lost`. A users-plane change (a policy row, a network set) is
checked against the running sandboxes' classes too: one whose class
narrowed is stopped as above. So are the workspace's own reasons, below.

**The workspace around them.** A running sandbox follows its tile's reach,
not only its next start — each of these stops it, synced, state kept, with
the reason in `stateDetail`:

- its tile is removed, disabled, hidden or offloaded, or loses
  `cap:sandboxes` — a revoke at once, a hand edit of `xbin.json` at the
  next rescan (a sandbox still starting then never comes up; its start
  answers 503);
- a mount the tile no longer holds (the grant revoked, the resource
  dropped from its manifest or deleted), or a read-write mount the tile
  now holds only as a reader — `stateDetail` names the mount; a role that
  widened waits for the next start;
- the vault sealed ("the vault was sealed…"), for every sandbox with a
  resource mounted, before the decrypted views go; its next start answers
  503 until the vault is unsealed. A `cap:containers` change of the scope
  (its resources remount) stops the scope's mounted sandboxes too;
- the disk (below).

`diskBytes` is measured — a namespace sandbox's upper and snapshots at
each stop and every 2 minutes while it runs, a VM's disks by their
allocated blocks. A tile whose sandboxes pass `perTile.diskGiB` while one
runs has its largest running namespace sandbox stopped ("the tile's
sandboxes use X GiB, over its N GiB (sandboxes policy: perTile.diskGiB)");
further starts are 429 until something is deleted. While the workspace
disk is low (the partition below diskmon's reserve), starts are 503 and the
running namespace sandboxes of every tile whose sandboxes hold more than
the fair share are stopped, largest tile first ("the workspace disk is
low…"); VM sandboxes, whose disks are bounded, run on. Sandbox bytes count
for the workspace's disk pressure, never against a scope's resource-write
quota.

A tile's backup carries its sandbox definitions, never their state. A
restore brings them back **by uid**, `stopped`: the same sandbox is
replaced and keeps its state; a name another sandbox holds now is left out
(listed in the restore's `sandboxesSkipped`); one that had state which is
gone comes back `error` ("restored without state — reset it…"). A tile's
offload is refused while its sandboxes hold state. A removed tile's
sandboxes stay — definitions and state — as leftovers of its path: a
non-admin can't create a tile there, and an admin deletes them with
`DELETE /sandboxes/<name>?tile=<path>`.

**Files and trees** go to the sandbox's own agent, which resolves every
path inside the sandbox: a symlink leads only to the sandbox's files, and
its read-only mounts refuse a write (400 `invalid`). xbind never resolves
one on the host.

- **Paths** are absolute and clean — no empty, `.` or `..` segment, no
  trailing `/` — and UTF-8 without NUL. A call's paths and `exclude`
  patterns together must fit the agent's 4 KiB request line: a path of a
  few thousand plain characters fits, and fewer where JSON escapes
  characters. Anything else is 400 `invalid`.
- **Running.** As for a command (Auto-start, above): a stopped sandbox
  with `autoStart` starts for the call, a `starting` one is waited for and
  a `stopping` one is waited out and started again, within `waitMaxSec`;
  without `autoStart` a stopped or stopping sandbox is 409 `state`. While a
  file, tar or copy call runs, the sandbox isn't idle, however long its
  stream takes.
- **`stat`** answers `{path, type: file | dir | symlink | other, size,
  mode, mtimeMs, etag, target?}`. A symlink is stated itself, not its
  target. `mode` is the permission bits in octal (`"0644"`). `etag` is
  `<inode>-<size>-<mtime ns>` in hex, so an atomic replace changes it.
- **Reading `content`** answers the bytes with `Content-Length` and
  `ETag: "<etag>"`, the stat's etag quoted. `offset` and `length` (0 = to
  the end) pick a range. A range over `limits.fileMax` is 413
  `too-large`. Only regular files are read: a device, FIFO or socket is
  `invalid`.
- **Writing `content`** streams the raw body into a temporary file and
  renames it into place (fsynced) only once all of it arrived, so a body
  cut short changes nothing. It answers the new stat.
  - `mode` sets the permission bits. Without it a replaced file keeps its
    mode, and a new one is `0644`. A replaced file keeps its owner too.
  - `mkdirs=1` makes missing parent directories.
  - `ifMatch=<etag>`, bare or quoted as the header gives it, replaces the
    file only while its etag is that one. `ifNoneMatch=*` only creates it.
    Either failing is 412 `precondition` with the current `etag`.
  - A body over `limits.fileMax` is 413, at once when its
    `Content-Length` says so.
- **`list`** answers `{path, entries: [{name, type, size, mtimeMs, mode,
  target?}], truncated}`, sorted by name. `limit` defaults to 1000; more
  than 100000 is clamped.
- **`mkdir`, `remove` and `move`** answer 204. `mkdir` without `parents`
  of a path that exists, and `remove` of a non-empty directory without
  `recursive`, are `invalid`. `move` onto an existing path without
  `overwrite` is 412 `precondition`, with the destination's `etag`.
- **Owners.** What these calls create (files, directories, tar entries)
  belongs to the sandbox's `defaults.uid`/`gid`.
- **`GET tar`** is the directory as a tar stream, with names relative to
  `path`.
  - Symlinks are stored as links and never followed. Hard links are
    stored as links. Devices, FIFOs and sockets are left out.
  - An `exclude` glob drops an entry whose name relative to `path`, or
    whose base name, matches it.
  - A tar of `/`, or of a symlink leading there, leaves out `/proc`,
    `/sys` and `/dev`.
  - It is bounded by `limits.tarMax`.
- **`PUT tar`** extracts the stream under `path`, which `mkdirs=1` makes.
  - No entry lands outside `path`: `../x` is skipped, and `/x` lands at
    `path/x`. A symlink an entry plants is never written through.
  - setuid and setgid bits are dropped, and devices aren't extracted.
  - A body over `limits.tarMax` is 413.
- **A stream that fails after its status went out** is cut: the
  connection closes before the body ends. This covers a tar passing
  `tarMax` and a read error part-way. Such a body never ends as if it
  were whole.
- **`POST /sandboxes/copy`** copies between two of the caller's own
  sandboxes, or within one. Naming another tile's sandbox is 404. The copy
  streams from agent to agent, bounded by `limits.tarMax`, and answers
  204.
  - A directory, or a symlink leading to one, is copied as a tree: its
    contents land at `to.path`. With `overwrite` they merge into what is
    there; without it, an existing `to.path` is 412.
  - Anything else is copied as a file, atomically. Without `overwrite`, an
    existing `to.path` is 412.
  - Missing parents of `to.path` are made.
  - A source that fails part-way leaves a file copy uncommitted. A tree
    keeps what was extracted by then.
  - Copying a tree into itself is 400.

**Snapshots and clones.** A snapshot is a copy of a sandbox's state — a
namespace sandbox's upper, exactly (whiteouts, opaque directories, owners,
modes and `user.*` xattrs; a copy that can't keep one fails), copied in a
confined run; a VM's disk, sparse, sharing its extents with the original
where the filesystem can. xbind never reads either.

- **Taking one** stops the sandbox (its execs end `killed`), copies, and
  starts it again if it ran. A sandbox that never ran has nothing to copy
  (409 `state`). Ids are `s-<n>`, never handed out twice for a sandbox.
  `clientId` makes it repeat-safe (per sandbox): the same `name` answers the
  snapshot (200), another is 409 `exists`.
- **A snapshot pins its base image**: the base it was built on stays
  installed while the snapshot exists, whatever the sandbox does next
  (reset, rebase, an upgrade).
- **A restore** puts the snapshot's state back, base included — `base`
  follows it, and a rebase moves it on again — and starts the sandbox again
  if it ran. It repairs `error`. The old state goes to a confined removal.
- **A clone** is `POST /sandboxes` with `from: {sandbox, snapshot?}`, a
  sandbox of the same manager, in its `mode`: of a snapshot, whatever the
  source does meanwhile, or — without `snapshot` — of the source's state,
  only while it is `stopped` (409 `state` otherwise: stop it, or clone a
  snapshot of it). It takes the source's base image; a VM clone's disk is
  never smaller than the source's. It counts against `perTile.max` from
  the moment it is defined.
- **Refused:** another `mode`, and in namespace mode an upper of the other
  overlay flavour (fuse-overlayfs's and the kernel's can't read each
  other's), are 400 `invalid`; so is a snapshot whose base image is no
  longer installed. A snapshot or clone whose bytes (the snapshot's, or the
  source's last measured) would take the tile's sandboxes past
  `perTile.diskGiB` — copies still running counted — is 429 `limit`.
  Snapshots count toward `diskBytes`.
- **Copies run off the request**, and are made whole or not at all: a
  crash or a restart leaves the old state, no half snapshot, and a clone
  it cut short in `error`. `?wait=<seconds>` (absent: `waitMaxSec`) bounds
  how long the call waits: done in time, the normal answer; not done, a
  snapshot answers 202 with `pending: true` (it lists so until it is
  done), a restore the sandbox with `stateDetail` `busy: restoring
  snapshot s-2`, a clone 201 `creating` — poll `GET`.
- **While a snapshot or a restore copies** (and while a clone copies a
  stopped source's state, the source) the sandbox is **busy**: its
  `stateDetail` starts `busy: `, and start, stop, reset, rebase, another
  snapshot, a restore, `DELETE` and every command or file call answer 409
  `state` with that `stateDetail` and `retryAfterMs` — but a command or
  file call on a sandbox with `autoStart` waits the copy out, as it waits
  out a stop. A snapshot a clone is copying, and its sandbox, aren't
  deleted meanwhile (409 `state`). The workspace's own reasons (a revoke,
  a seal, the policy switched off) find the sandbox stopped for its copy,
  and its start after the copy is refused as any start would be; xbind's
  shutdown ends the copy, leaves the sandbox stopped, and what it staged
  is removed.
- **A clone that fails** — its copy failed, or an xbind restart cut it
  short — is `error`, and answers 409 `state` to everything but `GET`, the
  list and `DELETE`.

**SandboxInfo:** `{name, uid, state (creating | stopped | starting |
running | stopping | error), stateDetail, mode, accel?, memMiB, vcpus,
diskGiB, net:{egress, reach, egressNext, note}, mounts, defaults, labels,
for?, forUser?, idleStopMin, autoStart, base:{version, outdated}, users,
diskBytes, snapshots, execsRunning, created, started?, lastActive?,
version, clientId?, restartNeeded}` — `uid` is the sandbox's identity (12 hex,
fixed at create; its name is only its address): a sandbox deleted and
created again under the same name gets another `uid`, and never the old
one's state. `creating` is a clone whose copy still runs: until it ends
`stopped` (or `running`, or `error`), every call but `GET`, the list and
`DELETE` answers 409 `state`. `reach` is what the egress reaches (`none`,
`internet` or `open`, the contract's words), `egressNext` an egress a
`PATCH` set that waits for the next start.

**The policy** (`.xbin/sandboxes/policy.json`; 0 = the default, a `PUT`
keeps the fields it doesn't name): `{enabled (true), perTile:{max 8,
running 4, memMiB 8192, vcpus 8, diskGiB 100}, perSandbox:{memMiB 2048,
vcpus 2, diskGiB 20, maxMemMiB 8192, maxVCPUs 8, maxDiskGiB 200, pids
4096}, total:{memMiB 0, pids 32768}, idleStopMin 30 (≤ 1440), outputRingMiB
1 (≤ 8), outputBudgetMiB 64, overrides:{"<tile>": {perTile, perSandbox,
…}}}` — `total` caps every tile sandbox together (`memMiB` 0 = ¾ of the
host's RAM) and has no per-tile override; an override replaces that tile's
previous one, `null` removes it. `runtime.limits.flows` is each sandbox's
cap on concurrent network connections (TCP and UDP flows through its relay);
past it a new one is refused at once. Turning `enabled` off stops every
running tile sandbox (state kept, `stateDetail` says so) and refuses
starts. **A policy file that can't be read fails closed:** `GET` shows why
in `error`, `policy.enabled` is false and so is `runtime.enabled`, and no
tile sandbox starts (503) — whatever the file said — until an admin's
`PUT` writes a new one (merged onto the defaults), which clears the
error.

**Commands** (`run`, `execs`) are the contract's, byte for byte
([sandbox-manager.md](sandbox-manager.md) §Running commands), plus `uid`,
`gid` and `forUser` (a claim, like the definition's):

- **What runs.** `argv`, or `cmd` run as `<defaults.shell or /bin/sh> -lc
  <cmd>` — one of them, never both. `cwd` defaults to `defaults.cwd`, else
  `/`, and must exist: a missing one is 400 `invalid`, never a fallback to
  `/`; so is an `argv` whose program can't start — but a start that
  fails because the sandbox itself ran out of processes, memory or file
  descriptors (its `pids.max`, say) is 429 `limit`, to retry once some of
  its processes ended. `uid`/`gid` default to
  the definition's (`users: root` allows only 0). `argv` and `env` together
  are at most 256 KiB (413). The environment is xbind's: `IN_SANDBOX=1`
  (always), `SANDBOX_ID` and `SANDBOX_NAME` (the sandbox's name), `HOME`
  (`/root` for root, `/` for any other user) and a `PATH` with the base
  rootfs's toolchains first (`/usr/local/{go,node,bun}/bin`, as terminals
  and backends have it), plus `PLAYWRIGHT_BROWSERS_PATH=/usr/local/ms-playwright`
  where the rootfs ships Playwright's browsers, with
  `defaults.env` and then the command's `env` over them; `XBIN_*` keys are
  400. Nothing of xbind's or of the agent's own environment gets in, nor
  xbind's user's supplementary groups: a command has none (except on a
  namespace host mapping a single uid, `users: root`, which can't drop
  them). Each command leads its own process group, and it is what the OOM
  killer takes first.
- **A stopped sandbox** with `autoStart` (the default) is started for a
  command, a `starting` one waited for and a `stopping` one waited out and
  started again (Auto-start, above); without `autoStart` a stopped or
  stopping one is 409 `state`. A run's `timeoutMs` doesn't count the
  start.
- **Limits.** A sandbox runs at most `runtime.limits.execsRunning` (16)
  commands at once — execs and runs together; past it 429 `limit`.
- **`run`** answers when the command ends: `{exitCode, signal, timedOut,
  ms, stdout, stderr}` (`output` with `merge`), each `{head, tail, elided,
  bytes}` — past `maxOutput` (default and cap `runOutputMax`, 1 MiB per
  stream) its first quarter in `head` and its last three quarters in
  `tail`; text is UTF-8 with invalid bytes replaced. `timeoutMs` defaults
  to 60000 (cap `runTimeoutMaxMs`); at it the process group gets TERM,
  then KILL 5 s later, and `timedOut` is true. A caller that hangs up has
  the group killed. `stdin` is a string (at most `stdinMax`). A run isn't
  listed.
- **Execs.** `POST …/execs` answers 201 once the command runs (200 when a
  `clientId`, per sandbox, repeats the same request; 409 `exists` for
  another). Its id is `<6 hex>-<n>`, the hex random per xbind start: an id
  from before xbind restarted is 410 `lost` — the only `lost` — and one
  this sandbox doesn't have is 404. An exec is `{id, label, cmd, argv,
  cwd, tty, state (running | exited | killed), exitCode, signal, started,
  ended, total, clientId, forUser, uid}`: `killed` when a signal ended it
  (`exitCode` null, `signal` names it), and when its timeout or a `DELETE`
  did. **A stop — the manager's, an admin's, or however the sandbox ended
  — ends its running execs `killed` with `signal: "KILL"`**; their records
  and output stay. A finished exec is kept an hour, and past that while it
  is one of the sandbox's last 50 (at most 1000 within the hour); deleting
  the sandbox forgets them all. `timeoutMs` (0 = none) sends TERM to the
  group, then KILL 5 s later.
- **Output.** A non-tty exec's stdout and stderr are one stream, a tty
  exec's is its terminal, kept in a ring of `limits.outputRing` bytes;
  the tile's rings share the policy's `outputBudgetMiB`, and past it the
  oldest finished exec's bytes go first (its `ringStart` moves). `GET
  …/output?since=&max=&waitMs=&encoding=` answers `{start, end, total,
  ringStart, data, encoding, state, exitCode, signal}`: the bytes from
  `since` (or the ring's oldest: `start > since` is a gap), at most `max`
  (default 64 KiB, cap 1 MiB); with nothing past `since` while the exec
  runs it waits up to `waitMs` (cap 30000) and answers as soon as the exec
  ends. `text` never splits a character across two reads (`end` stops
  before it) and replaces invalid bytes; `base64` is exact. A `since` past
  `total` is 400.
- **stdin** (`POST …/stdin`, the raw body, at most `stdinMax`) goes to an
  exec started with `stdin: true`, or to a tty exec's terminal; `?eof=1`
  closes stdin after it (a tty's is 400: send `^D`). Without `stdin: true`
  it is 400, after the exec ended 409 `state`, and a command that doesn't
  read it for 30 s answers 503. **signal** takes `{signal: INT | TERM |
  KILL | HUP, group?}`: `group` defaults to true, so a forwarded
  `{"signal":"INT"}` reaches the whole process group. **resize** is a tty
  exec's (`{rows, cols}`, 1…65535). Both are 409 `state` once the exec
  ended. `DELETE …/execs/<id>` kills the group and forgets the exec.

**The TTY routes** are WebSockets on exactly the `/ws/term` wire (below):
binary frames both ways (the ring's tail replays first, at most 256 KiB),
`{"op":"session","id":…,"sandbox":…,"echoAck":true}` first, acks and
pongs, and `{"op":"exit","code":N}` at the end (`code` null and a `signal`
when a signal ended it); the client sends `resize` and `ping`. `sessionId`
and `sandboxId` (`[A-Za-z0-9._-]{1,64}`) replace the session frame's `id`
and `sandbox`, so a manager relaying the bytes shows its consumer its own
ids. `GET …/tty` starts a tty exec — the login shell (`<shell> -l`), or
`cmd` — labelled `terminal` and listed under `execs`, and attaches; an
attach to a tty exec that ended replays its ring and says `exit`. A client
that leaves doesn't end the command. Refusals come before the upgrade, as
JSON: a request that isn't a WebSocket upgrade is 400, and so is an attach
to an exec without a terminal. Only the manager's instance token reaches
them — no person does; the manager relays the socket to its consumer's
page. A tty exec whose `forUser`, or an attach whose `forUser`, names a
user with `noTerminal` (D88) is 403, checked again at every attach, and a
user's `noTerminal` taking effect — switched on, or an admin who had it
set demoted (by the users API, an org role or SSO) — kills the tty execs
claimed for them and those they attached to; non-tty execs aren't
restricted by it. One tty exec is attached for at most 64 distinct
`forUser` values; a new one past them is 429 `limit`.

## WebSockets

### `/ws/term` — terminals (admins + users with a terminal-level tile)

```
GET    /ws/term?cwd=<p>|session=<id>   WebSocket upgrade → a terminal session (below)
                                   &deployment=<name>: a new session's
                                   target (ignored on reattach); the session
                                   control frame echoes deployment (below)
DELETE /ws/term?session=<id>       end a session now (creator or admin) → 204
DELETE /ws/term/env?cwd=<p>        terminal level on the tile: wipe its persistent
                                   terminal layer back to the base rootfs → 204
                                   (on a partitioned tile: the caller's own)
GET    /ws/term/env?cwd=<p>        that layer's state → {exists, baseOutdated,
                                   vm:{available,reason,memMiB,vcpus}}
```

Connect with `?cwd=<component-path>` (new session) or `?session=<id>`
(reattach; scrollback replays first). A session may only be opened on a tile
where the caller's access level is **terminal** (docs/auth.md), mounts its
creator's `$HOME`, and carries a per-session `XBIN_TOKEN` scoped to that tile
(docs/overview/09-terminals.md). Reattaching re-checks that level: a creator
whose terminal level on the tile was withdrawn since gets 403 ("revoked")
until the session ends. Under `--isolate` the workspace mounts read-only
(all tiles' source — for a non-admin, minus tiles below their read level,
which are masked out) with `.xbin/`, `data/`, and other users' `homes/`
**masked out** (docs/isolation.md), so the terminal can't read the owner
token or resource state. **The root terminal (no cwd) is disabled** — 403 for
everyone. Reattach/kill of another user's session: admins only. Which
sessions are yours on a tile: `GET /api/xbin/term/sessions?cwd=` (the
session directory, D73) — a browser keeps no session ids of its own. An
**agent session** (`kind:"agent"`, created on `POST /api/xbin/term/sessions`)
has no terminal socket: `?session=<agent id>` answers **409** — drive it
through the agent routes above.

For a **non-admin**, the query params below are clamped rather than honored
(docs/isolation.md): `api` is forced to `0` without the `termApi` grant; on a
personal/workspace tile `net` is forced to `none` without `termNet` and
`net=host` is admin-only; on an **org-owned tile with network sets** (D54)
the org network is the members' grant — `internet` and a refused `host`
clamp to `org`, and `host` is allowed when a set says so; a `set:<name>` the
caller may not pick there clamps to the tile's default. The session still
opens; the `session` control frame reports the effective scope, the scopes
the caller may pick, and why a request was clamped (`netNote`). New-session
query params (all optional):

- `?api=0` — mint **no** terminal token: the shell sees code but every tile/xbin
  API call is unauthorized (default `1`).
- `?net=<scope>` (absent = the tile's default: `org` where it exists, else
  `internet` where allowed, else `none` — `GET /term-net?tile=` lists them):

- `org` — own network namespace with an egress relay enforcing the owning
  org's **network sets** (the union of their rules — LAN ranges, named
  internet destinations with DNS pinning, all internet); when a set says
  `host` this scope is host networking. Org-owned tiles only.
- `set:<name>` — one named network set (D65): the relay under exactly its
  rules, host networking when it says `host`. Each set attached to the
  tile's org, for whoever may open a terminal there; any workspace set for
  a workspace admin, on any tile. An unknown or unpickable name clamps to
  the tile's default (`netNote` says so). Never the default.
- `internet` — own network namespace with an **internet-only egress relay**
  (`net:internet`: public addresses only, no host interfaces visible). TCP, UDP,
  and ICMP echo (`ping`) are forwarded under the policy; `traceroute` needs the
  `host` scope. xbind stays reachable at `$XBIN_URL` via the relay's gateway
  host-forward, so `bx`/`curl` work.
- `host` — share the host network (LAN + host services reachable, host
  interfaces visible). The escape hatch.
- `none` — an isolated namespace with no egress (xbind unreachable).

A new session also takes `?deployment=<name>`, its **target**: the tile
deployment ([tile-deployments.md](/docs/tile-deployments.md)) its
`XBIN_TOKEN`'s self-calls and self-scoped calls reach. It is fixed for the
session's life (changing it restarts the session) and ignored on reattach.
The primary's name means "follow the primary"; a protected primary answers 403
`the primary of <tile> is protected: terminal and agent sessions can't target
it`, an unknown name 404, a malformed one 400. Without it the session follows
the primary — unless the primary is protected: then it targets the live
reload target, and when there is none (live reload paused) it opens with no
tile API. A session following the primary follows a reassignment without a
restart; protecting the primary restarts it onto that default (a shell is
ended with a notice), and a reassignment restarts the sessions named after
either deployment. A removed target makes the session's calls answer 404;
the session isn't killed. `XBIN_DEPLOYMENT` is set in a session whose target
is a named deployment other than the primary at start, and absent otherwise.
`api=0` (and a user without the `termApi` grant) means no target at all.

A new session also takes `?gpu=<none|all|index|uuid>` (default `none`) to bind
host NVIDIA GPU(s) into the terminal's dev sandbox (owner plane; no grant
needed). Enumerate host GPUs at `GET /api/xbin/gpus` (admin).

`?vm=1` opens the session in a **VM sandbox** (D89,
[isolation.md](isolation.md) §VM sandboxes): a Firecracker microVM with its
own kernel, where the shell is root, running inside the same namespace
sandbox as the jail. The same mounts appear at the
same paths (served from outside the VM), the same network scope applies (the relay
enforces it outside the VM), and `$XBIN_URL`/`XBIN_TOKEN` work unchanged.
It needs `--isolate`, KVM (or the shipped emulation — much slower; `GET
/ws/term/env` then reports `vm.emulated` and `vm.note`, D90), and an admin
who turned VM terminals on (`PUT /vm/policy`); otherwise the upgrade fails
with 400 and the reason (`GET /ws/term/env` reports `vm.available` and
`vm.reason` beforehand). `net=host`
and `gpu` can't combine with `vm=1` (400). A VM terminal's root filesystem
changes are kept on the tile's VM disk (in its terminal layer — the same
lock and Reset as the namespace layer, a separate filesystem).

The scope is fixed at spawn; switching net, GPU, VM or the target restarts the
session (the UI ends the old one and opens a new WS).

The frames are [the terminal wire](#the-terminal-wire) (below), with
`/ws/term`'s own fields on its `session` frame:

- **Binary frames** both directions: raw PTY bytes.
- **Text frames**: JSON control.
  - server → client: `{"op":"session","id":"…","net":"org","label":"org network
    (devs-net)","scopes":[{"id":"org","label":"…","desc":"…"},…],"netNote":"…",
    "baseOutdated":false,"vm":false}` (first message — on every socket,
    including one attached to a session that has already ended, where the
    scrollback and the exit follow it; `vm` = a VM sandbox; `scopes` = what this caller may
    pick on this tile, `label` names the effective scope, `netNote` explains a
    clamp; `baseOutdated:true` ⇒ this terminal's persistent layer was built on
    an older base image — reset it via `/ws/term/env` to rebuild on the
    current base; `echoAck:true` ⇒ this xbind sends the `ack` and `pong`
    frames below; `deployment` = the named target, or the current primary's
    name for a session that asked for the primary by name — the echo a client
    checks, since an older xbind ignores `?deployment=`; `targetNote` = a line
    to print like `netNote`: `this terminal calls <tile>+<name>`, or `tile API
    off: <primary> is protected and live reload is paused`; `api:false` ⇒ the
    session asked for or was given a target but has no tile API (the
    protected-primary fallback, or a user without the `termApi` grant). All
    three are absent for a session that sent no `deployment` and follows the
    primary. On a partitioned tile only, the frame adds `partition`
    (`user:<id>` | `global`: what the session acts in), `partitionNote` (a
    line to print like `netNote`: `partition: yours (user:<id>) · the tile
    directory is shared code — keep your own files in $HOME`; `partition:
    user:<id> · no isolation: this shell runs on the host and can read every
    partition's data` on an xbind without `--isolate`; or `tile API off:
    <why>`) and `api:false` when the session has no person and the tile
    no global instance; a tile sandbox's TTY, which speaks this wire too, adds
    `sandbox` — the sandbox's id — so ignore fields you don't know),
    `{"op":"exit"}` — the process ended (here, the shell), and the socket
    closes after it. It may carry `"code":N`, or `"code":null` with
    `"signal":"KILL"` when a signal ended the process (a tile TTY does; a
    shell's exit here is bare). It is sent **only** when the process ended: a
    socket that falls too far behind the output is closed **without** one —
    the session lives on, so reattach (`?session=`) and the scrollback
    replays. `{"op":"ack","n":N}` —
    the client's Nth **binary** frame on this socket reached the PTY at least
    50 ms ago, so whatever the application printed in answer precedes this
    frame (one ack covers every earlier frame; mosh's echo ack, the basis of
    the terminal's predictive echo, D70), `{"op":"pong","t":…}` — answers a
    ping, `t` echoed verbatim.
  - client → server: `{"op":"resize","cols":120,"rows":32}`,
    `{"op":"ping","t":<any JSON>}` (the browser measures its round trip; a
    WebSocket-level ping is answered below JavaScript). Unknown ops are
    ignored on both ends.

`DELETE /ws/term?session=<id>` ends a session immediately (creator or admin;
used by the UI to restart under a new scope); `204` on success, `404` unknown.

**On a partitioned tile** ([partitions.md](/docs/partitions.md) §Terminals and
agent sessions) a new session belongs to its opener's partition: its
`XBIN_TOKEN` acts there (§Authentication), `XBIN_PARTITION=user:<id>` is set
right after `XBIN_COMPONENT` (after `XBIN_DEPLOYMENT` when that is set;
`global` for a session targeting a non-primary deployment), it starts in its
`$HOME` instead of the tile directory, and its persistent layer is the
person's own (`.xbin/term-part/<tile-key>/<partition-id>/`, never backed up;
`/ws/term/env` resets and reports that one). A session without a person (the
owner token) acts in the global instance, or opens with no tile API when the
tile has none (the session frame's `api:false`). An admin reattaching to
another person's session there gets 403 `session belongs to another user:
<tile> keeps each person's data apart, so an admin can't open other
people's sessions there (ending one is allowed)`; ending it stays allowed. A
session asked for while the tile's partition mode switches answers 409 (as
does a person's while their partition of the tile is being reset or
removed); a switch that deletes the tile's data ends its sessions first —
those opened on a sub-path of the tile too — and deletes the person layers,
and a tile whose recorded mode gains or loses user partitions ends the
sessions opened under the other mode. Every other tile's sessions are
unchanged.

`DELETE /ws/term/env?cwd=<component-path>` (terminal level on that tile; the
root layer — `cwd` empty — admin-only) wipes that component's
**persistent terminal layer** (installed packages / system changes) back to the
base rootfs, killing any live session on it first; `204` on success, `500`
with the layer untouched when a session doesn't end within 5 s. Each
component's terminal has its own persistent overlay layer (`.xbin/term/<key>/`)
so system-level changes survive across sessions — a resettable dev sandbox
(`docs/isolation.md` §The dev layer). Workspace files and `$HOME` persist independently.
`GET /ws/term/env?cwd=<component-path>` (same gate) reports that layer
without opening a terminal: `{"exists":bool,"baseOutdated":bool}` —
`baseOutdated` as on the session frame, so the terminal window offers the
base update on its session chooser too.

Sessions survive disconnects; idle unattached sessions are reaped after 24 h;
xbind restart kills them (run `tmux` inside if you care).

### The terminal wire

`/ws/term`'s framing is the one terminal protocol in xbin, and other
endpoints speak it too: a sandbox manager's `tty` routes
([sandbox-manager.md](sandbox-manager.md) §Terminals), a tile's own pty
route for the xbin app's `terminal` ([native.md](native.md) §Escape
hatches), and whatever `<bx-terminal src>` is pointed at
([elements.md](elements.md) §`<bx-terminal>`). Speak exactly this, and those
clients work against your endpoint:

- **A WebSocket.** Refusals come before the upgrade, as plain HTTP (a
  browser page never sees them: the socket just closes, so a client checks
  what it can beforehand).
- **Binary frames**, both ways: the terminal's bytes. Keystrokes arrive as
  typed (Enter is `\r`); output is sent as it comes — on (re)attach the
  endpoint may first replay what it kept (the scrollback, a ring).
- **Text frames** are JSON control, `{"op": …}`. Unknown ops, and unknown
  fields, are ignored on both ends.
  - server → client, first: `{"op":"session","id":"<session id>",
    "echoAck":false}` — `id` names what a client reattaches to (`/ws/term`
    `?session=`; a manager `…/execs/{id}/tty`); an endpoint adds its own
    fields (`/ws/term`: the scope; a manager: `sandbox`).
  - `{"op":"pong","t":…}` answers every ping, `t` echoed verbatim.
  - `{"op":"exit","code":0}` once the command has ended and its output is
    out (`code` null and `"signal":"KILL"` when a signal ended it; bare
    `{"op":"exit"}` is fine), then a close with 1000. A clean close (1000,
    or none) with no exit frame also means it ended.
  - `{"op":"ack","n":N}` only with `echoAck:true` in the session frame: the
    client's Nth binary frame on this socket reached the terminal at least
    50 ms ago, so the output it caused precedes this frame (the client's
    predictive echo, D70). Without `echoAck` the client doesn't predict.
  - client → server: `{"op":"resize","cols":120,"rows":32}` — first on every
    connect, and whenever the client's grid changes; it takes effect before
    the keystrokes sent after it. `{"op":"ping","t":<any JSON>}` — the
    client measures its round trip (a WebSocket-level ping is answered below
    JavaScript).
- **Leaving.** A client that disconnects doesn't end the command; ending it
  is the endpoint's own route (`DELETE /ws/term?session=`, a manager's
  `DELETE …/execs/{id}`). Any close other than a clean one (a drop, 1001,
  an error code) is a client's cue to reconnect with backoff.

### `/ws/events` — event stream

```
GET    /ws/events?frame=<token>    WebSocket upgrade → the event stream (below;
                                   ?frame= only for elements — humans use the cookie)
```

Auth: cookie (owner), a bearer session (`Authorization: Bearer` — the xbin
app's own socket, with its device session: the human's events, never handed
to a tile; D99) or `?frame=<frame-token>` (element; standalone — no cookie
required). JSON text frames:

```jsonc
{"type":"reload","component":"apps/thing"}          // source changed
{"type":"build-start","component":"apps/thing"}
{"type":"build-error","component":"apps/thing","text":"compiler output"}
{"type":"build-ok","component":"apps/thing"}
{"type":"grants"}                                    // grant table changed
{"type":"branding"}                                  // the workspace title/icon changed (D76): re-read GET /branding
{"type":"native"}                                    // the native-runtime switch changed (D101): re-read whoami (native.runtime)
{"type":"policies"}                                  // a workspace policy changed (PD-55): re-read GET /workspace-policies
{"type":"partitions","component":"apps/x","partition":"user:<id>", // to that person's own sockets (never apps/x's frames,
 "data":{"op":"consent-needed","from":"apps/z","to":"apps/x"}}     //   terminals or instance): apps/z's call into their apps/x data was refused (partitionConsent on; once a day)
{"type":"partitions","component":"apps/x","partition":"user:<id>", // to that person's own sockets, likewise: their consent changed
 "data":{"op":"consent","from":"apps/z","to":"apps/x","allowed":true}} //   (POST/DELETE /partitions/consents)
{"type":"partitions","component":"apps/x","partition":"user:<id>", // to that person's sockets and apps/x's credentials acting in their partition:
 "data":{"op":"state","partition":"user:<id>","event":"build-ok"}} //   their instance's build-start|build-ok|build-error|reload (text?: the detail)
{"type":"partitions","component":"apps/x",                         // to apps/x's readers (and admins, and apps/x's own credentials):
 "data":{"op":"mode","tile":"apps/x","state":"pending","spec":{"user":false,"global":false},
         "request":{"spec":{"user":true,"global":true},"since":"…","declined":false}}} // its mode changed; re-read GET /partitions?tile=
{"type":"partitions","component":"apps/x",                         // to the person's own session, app or device only:
 "data":{"op":"notice","notice":{"id":"…","at":"…","kind":"partition-deleted|partition-reset|credential","tile":"apps/x","text":"…","hold":"…"}}}
{"type":"bus","topic":"res:<scope>/<name>/<topic>","data":…}
{"type":"status","component":"apps/thing",           // a tile reported its condition
 "data":{"level":"error","message":"…","ts":1785…,"transient":false}}
{"type":"term","component":"apps/thing",             // a terminal session of yours was
 "data":{"op":"open|close|rename","id":"…","user":"…"}} // opened/ended/renamed (D73): re-list
{"type":"term","component":"apps/thing",             // an agent session of yours changed state:
 "data":{"op":"status","id":"…","user":"…","status":"waiting_permission",
         "pending":1,"questions":0,"turn":3}}        // its summary, inline — no re-list needed
{"type":"session","topic":"session.<id>","component":"apps/thing", // an agent session event (D74):
 "data":{"seq":7,"ts":1789…,"type":"message.delta","data":{…},"user":"…","id":"<id>"}}
{"type":"deployments","component":"apps/thing",      // the tile's deployment record changed (paused,
 "data":{"op":"record","seq":19,"by":"user:ana",     //   resumed, …): what names the fields
         "what":["liveReload","deployments"]}}
{"type":"deployments","component":"apps/thing",      // a deploy attempt's result or phase
 "data":{"op":"deploy","id":43,"deployment":"main","how":"reload-now","checkpoint":"c:3f2a1c9",
         "result":"running","phase":"build","by":"user:ana","session":"<id>"}}
{"type":"deployments","component":"apps/thing",      // while live reload is paused: files the work
 "data":{"op":"work-tree","changed":3}}               //   tree differs in from the checkpoint
{"type":"deployments","component":"apps/thing",      // the work tree's branch left (or came back to)
 "data":{"op":"branch","deployment":"dev","assigned":"feature", //   the one deployment requires (D131):
         "workTree":"release","related":"qa","paused":true}} //   related is assigned workTree's
{"type":"deployments","component":"apps/thing",      // a non-primary deployment's frames reload once
 "data":{"op":"reload","deployment":"dev"}}
{"type":"deployments","component":"apps/thing",      // a non-primary build of the work tree or restart;
 "data":{"op":"build","deployment":"dev","phase":"error","text":"compiler output"}}  // phase: start | error | ok
{"type":"deployments","component":"apps/thing",      // a seed, reset or restore moved
 "data":{"op":"data","deployment":"dev","busy":"","state":"seeded"}}
{"type":"deployments","component":"apps/thing",      // a non-primary deployment reported its condition
 "data":{"op":"status","deployment":"dev","level":"error","message":"…","ts":1790…,"transient":false}}
{"type":"deployments","component":"apps/thing",      // a notification it held instead of pushing
 "data":{"op":"notify","deployment":"dev","to":"user:bob","title":"…","at":"…"}}
{"type":"prefs","component":"root",                  // one of YOUR pref buckets changed:
 "data":{"key":"layout","writer":"…"}}               // re-read GET /prefs/<key> if you care
```

**Tile deployments** ([tile-deployments.md](/docs/tile-deployments.md)).
Today's types speak only of a tile's primary, whichever deployment it is,
with the bare path; no event ever carries a qualified `component`, and
everything about another deployment rides the `deployments` type, which
names it in `data.deployment`. A save in a tile whose live reload is paused
publishes no `reload` and rebuilds nothing; op `work-tree` reports, at most
once every 2 s per tile and only when the number changed, how many files the
work tree differs in from the checkpoint live reload was paused at (the
state's `workTree.changed`). A save while live reload follows a non-primary
deployment sends op `reload` and op `build` phases for it, and nothing of
today's types. A deploy of a checkpoint — reload now, deploy, promote, roll
back, and the pin that pausing, attaching elsewhere, protecting or
reassigning takes — rides op `deploy` on each phase (`checkpoint`,
`materialize`, `build`, `start`, `swap`) and result (`queued`, `running`,
`ok`, `failed`, `cancelled`), never `build-*`, and never carries compiler
output (op `build`'s `text` does, for a non-primary build, as `build-error`
does for the primary). After a swap that changed the code a deployment
serves, the primary gets one bare `reload`, another deployment op `reload`.
A resume or attach onto the primary builds the work tree with today's
`build-*`; a restart of a non-primary deployment's code sends op `build`. A
reassignment of the primary sends op `record` and one bare `reload`, since the
bare URL now serves other code; a lifecycle change sends the bare `reload` and
op `reload` for each non-primary deployment. `session` names the terminal or
agent session that acted, so its own window can skip the line it would
print. A tile's status also clears when a deploy swaps the code its primary
runs (a deploy emits no `build-start`); a failed deploy leaves it. A
non-primary deployment's tile-report and held notifications ride ops `status`
and `notify`, never `status` or a push. Op `branch` (`branches/1`): a tile
with an assigned branch saw the work tree's branch change — `deployment` is
the one live reload follows (or last followed), `assigned` its branch (`""`
for none), `workTree` the work tree's now (`""` for none), `related` the
deployment assigned `workTree` (`""` for none), and `paused` whether this
save paused live reload on `deployment` (it deployed nothing). A client
offers to follow: attach live reload to (or resume it on) `related`, resume
it on `deployment` once `workTree` is its branch again, or else keep
`deployment` on `workTree` this time (`confirm: "other-branch"`) or add a
deployment for it. Old clients ignore the unknown type.

`prefs` events are per-user and not even admins see another user's: the
bucket's owner's human sessions (browsers, the app) get the events of all
their buckets — `component` names the bucket (`root` = the shell's) — and a
tile principal (frame token, terminal, backend) only its own bucket's, the
reach `GET /prefs` gives it. `data.writer` is the `X-Prefs-Writer` header of
the write when it had one (absent otherwise). The shell skips its own writes
by that id and reloads its layout when another client (the app) wrote it.
A write to a tile deployment's bucket beyond `main` (above) is heard only by
that deployment's own principals, those whose `GET /prefs` reads the bucket:
never by main's frames or the user's human sessions, since today's types
don't speak of another deployment. Main's bucket's events never reach
another deployment's principals. The event is the same, with the bare tile
path.

Non-bus events go to every subscriber, except `prefs` (above) and `term` and `session` events
(D97), which reach the session's owner (`data.user`) — their signed-in browsers,
and a shell's terminal token for the sessions on its own tile — and admins;
never a tile (its frame token names the user it runs for, its backend's
token no one: neither follows anybody's sessions) — re-list `GET
/term/sessions` on a `term` one; the id and op are enough to update a tab
bar in place. An agent session also sends `term` op `status` whenever its
summary changes — `status` (starting \| idle \| running \|
waiting_permission \| cancelling \| error \| exited), `pending`
(unanswered permission requests), `questions` (unanswered elicitations),
`turn` (prompts taken, so a turn that ran and finished between two
summaries still shows as a change) — coalesced over a few tens of
milliseconds and never repeated; the last one precedes the `close`. It is
what an inbox follows ("waiting for you", "done") without following each
session's log; `GET /term/sessions/<id>` has the requests themselves. Older
clients that re-list on every `term` event keep working (one more re-list
per change). `bus` events
are delivered only to
the owner and to elements holding a reader grant on the resource.
A `bus` event of a deployment's data beyond `main` carries `deployment`
(`{"type":"bus","topic":…,"deployment":"dev","data":…}`) and reaches admins
and the credentials bound to that deployment's data only — never a
primary-bound principal of the same scope. `term` events (open, rename) of a
session with a named target carry `deployment` in `data`.
`deployments` events are filtered by the tile and the deployment they name,
at your current level on each delivery. A fact about the tile's primary
reaches everyone who may read the tile: ops `record` and `deploy` (onto the
primary) in their full form only to its write audience (admins, people with
write, the tile's own terminal and agent sessions while their user has
write), and to a non-primary deployment's own frame and backend tokens when
the deploy came `from` it; in a reader form everyone else (`record` with only
the reader view's fields in `what`, sent only when one changed; `deploy`
without `id`, `how`, `from` and `session`). Ops `work-tree` and `branch`
reach the write audience. Anything naming another deployment — ops `deploy`, `reload`,
`build`, `data`, `status` and `notify` about it — reaches only the write
audience and that deployment's own frame and backend tokens: never the
primary's frame token (minted for readers too), never another tile. `status`
events broadcast like the build events (the shell renders each only for tiles
it shows; the `GET /tile-report` snapshot below is read-filtered per caller).
Slow consumers are disconnected; reconnect with backoff (the bundled clients
do).

**Partitioned tiles** ([partitions.md](/docs/partitions.md)). An event that
belongs to a person's partition carries `"partition":"user:<id>"` — a `bus`
event in that partition's data, that partition's `status`, runner and
partitions events — and reaches only that partition: the person's own
sockets and the tile's credentials acting in it, never admins' (there is no
blanket admin pass for them), another person, another tile or view-as. A
`bus` one still needs the reader grant. A `bus` event on a partitioned
scope's own (not shared) bus that the global instance published carries
`"partition":"global"` and likewise reaches only subscribers acting in the
global instance there, with no admin pass. `term` and `session` events of
a partitioned tile reach only the session's person (their browsers, and
their terminal on the tile): admins don't receive them there. Events
without `partition` — every event of a tile that isn't partitioned, a
shared bus's, and the global instance's others — are delivered as above;
a partitioned scope's shared bus reaches another tile's credential acting
in a person's partition only while that person can read the scope.

### Agent session events

An agent session's log (`GET /term/sessions/<id>/events`) and its live
`session` events carry the same entries: `{seq, ts, type, data}` — `seq`
from 1 per session, `ts` unix milliseconds. A client renders from the
replay and applies live events by `seq`; on a skipped `seq`, a socket
reconnect, or a tab becoming visible it re-fetches `?since=<last>` (the
hub drops a slow subscriber rather than queue for it).

A resumed session's replay (the agent's `session/load` streams the earlier
turns back while the session is `starting`) is logged like any events but
not published one by one: when it is over, the hub carries one `session`
event `{seq:0, type:"replayed", data:{first, last}}` (the replayed seqs),
then the status that ended it. Re-read the tail (a page, or `?since=`) on
it. A client that ignores it still catches up: the status after it skips
`seq`s (xbind before 2026-09-28 published each replayed entry).

**Pages** (D130). A client need not replay the whole log (up to 5000
events): `?limit=<n>` (default 200, at most 5000) is the tail page,
`?before=<seq>&limit=<n>` the page before a seq — pass the previous page's
`nextBefore`. Either parameter selects a page; without both the replay
above is unchanged. A page is cut where a fold may start: never inside a
message or thought run, a tool call and its updates (and a subagent's
calls and text under it), a request and its answer, a turn's plan, or a
turn's end and its `files.changed` — nor after anything that may still
change (the running turn's unfinished calls and unanswered requests, the
text being written, a snapshot not yet reported). So folding the pages
one by one gives the blocks the whole log gives; a card longer than
`limit` makes its page longer. `{events, hasOlder, nextBefore, truncated,
next, last, state}`: `hasOlder` the ring holds events before the page,
`nextBefore` its first seq (0 when none), `truncated` the oldest page of a
ring that dropped earlier events, `next` its newest seq (the tail page's
is the follow cursor), `last` the log's newest. `state` is what a fold
holds before the page: `status` (a status event's data — the last status
before the page, with `status`, `detail`, `currentMode`, `options`,
`modes` and `commands` each from the latest status that carried it: fold
it first, as a status event), `usage` and `turn` of the last `turn.end`
before it, and on the tail page of a live session the `permissions` and
`elicitations` waiting now (they are in its events too). An older xbind
ignores both parameters and answers the whole replay — a client tells
them apart by `hasOlder`.

| type | data |
|---|---|
| `message.delta` | `{role:"user"\|"agent", text, messageId?, parent?, attachments?}` — a prompt is logged as one `user` delta, so every client sees it, with `attachments:[{name, mime, size, inline?}]` when it carried files (their bytes are not logged; `inline` true when the model got it with the prompt — an image block, an embedded text — else it is a file the agent was pointed at); agent text arrives in runs (a burst of tokens is coalesced into a few events); `parent` is the subagent tool call (`tool.call` with `subagent`) the text came from |
| `thought.delta` | `{text, parent?}` — the agent's reasoning, when it shares it |
| `plan` | `{entries:[{content, priority, status}]}` — the whole list, replacing the last |
| `tool.call` | `{id, title, kind, status, content?, locations?, rawInput?, rawOutput?, name?, label?, parent?, subagent?, planReview?, output?, outputDelta?, exitCode?}` — kind: read \| edit \| delete \| move \| search \| execute \| think \| fetch \| switch_mode \| other; content items are `{type:"content", content:{type:"text", text}}` (often markdown — the adapters fence command output), `{type:"diff", path, oldText, newText}` or `{type:"terminal", terminalId}`. The rest is lifted from the adapter's `_meta` so a client needs no per-agent code: `name` the tool's own name (`Bash`, `ExitPlanMode`, …); `label` a human headline for the call when the harness wrote one (Claude's description of a shell command — the `title` is the command); `parent` the subagent call this one runs under; `subagent` true on a subagent (Task/Agent) call itself; `planReview` true on Codex's plan approval; `outputDelta` a chunk of a shell command's output (append), `output` its whole output (replace), `exitCode` its exit status |
| `tool.update` | `{id, …}` — a partial update of that call (`status`: pending \| in_progress \| completed \| failed \| cancelled); `content`/`locations` replace, `outputDelta` appends to the output, `output` replaces it; a command's output chunks arrive in runs (`{id, outputDelta, parent?}` updates of one call are coalesced like the agent's text) |
| `permission.request` | `{pid, toolCall:{id, title, kind, rawInput?, content?}, options:[{optionId, name, kind}], rule:{kind, title, scoped}, meta?}` — kind: allow_once \| allow_always \| reject_once \| reject_always; answer on `POST …/permissions/<pid>`. `rule` is what "allow for the session" would remember (`scoped:false` = no session rule is possible and the clients hide that choice: the call has neither kind nor title, or it is a `switch_mode` — a plan approval, whose allow_always options are modes, never remembered); `meta` is the adapter's presentation hint when it sends one (`{title, description, defaultToNo}`) |
| `files.changed` | `{toolCallId?, turn?, changes:[{path, oldPath?, status, add, del, binary?}], patch:{format:"git_patch", text, truncated}}` — what a finished tool call (`toolCallId`) or a whole turn (`turn`, after its `turn.end`) changed in the tile, from snapshots of the work tree (tiles that are git repos; the tile's own repo, index and HEAD are never touched). status: added \| modified \| deleted \| renamed \| typechange; `patch` is a git patch capped at 64 KiB per call, 192 KiB per turn (`truncated`) — `GET …/diff?toolCallId=|turn=` serves the whole of it (and one file of it) while the session lives. Not sent for edit/delete/move/read/search calls (an edit reports its own diff) nor when nothing changed |
| `elicitation.request` | `{eid, toolCallId?, message, schema}` — the agent asks the user a question (ACP `elicitation/create`, form mode — Claude's AskUserQuestion, an MCP server's form); the session is `waiting_permission` until one client answers on `POST …/elicitations/<eid>`. `schema` is a flat JSON Schema object: a `oneOf`/`enum` string is a single choice (options `{const, title, description?}`), an array of `anyOf`/`enum` items is a multi-choice, plus plain string/number/integer/boolean fields; a string field whose `_meta._askUserQuestionCustomAnswer.questionId` names another field is that question's free-text "Other" answer. Cancelling the turn answers `cancel` |
| `elicitation.resolved` | `{eid, action, by, content?}` — action: accept \| decline \| cancel; `content` the submitted values (with accept) |
| `permission.resolved` | `{pid, optionId, by}` — by: `user:<id>`, `owner`, `auto` (a session rule), `cancel` |
| `turn.end` | `{turn, stopReason, usage?:{used, size, cost?}, error?}` — stopReason: end_turn \| max_tokens \| max_turn_requests \| refusal \| cancelled \| error |
| `status` | `{status, detail?, modes?, currentMode?, options?, commands?, agent?, login?, usage?, title?}` — status: starting \| idle \| running \| waiting_permission \| cancelling \| error \| exited; `title` is the agent's own name for the session (ACP `session_info_update` — most adapters generate one after the first turn); it names a session that has no name yet (SessionInfo `name`, announced by a `term` `rename` event) — a name the user gave is kept; `modes` (the agent's available modes), `options` (its settings: `[{id, name, category, type, currentValue, options:[{value, name}]}]` — model, effort, …, in the agent's priority order) and `agent` (`{name, version}`) ride every `idle`; `options` also rides a status whenever a setting changes; `commands` (the agent's slash commands, `[{name, description?, hint?}]` — `hint` says what to type after the name; a command is sent as ordinary prompt text, `/name args`) rides a status when the agent advertises them and every `idle` after; `login` (`{needed:true, provider, command}`) rides every status while the agent reports it is signed out (an `_auth/status_update{kind:none}`) or a turn hit auth-required — the frontend shows a one-click sign-in that runs `command` in a shell terminal sharing the agent's home; an `error` names what to do (no login → the command to sign the CLI in from a terminal) |
| `gap` | `{before}` — only on a `?follow=1` stream: the cursor predated the log's ring; earlier events were dropped |

The live log is in memory; an `exited` or `error` status is final and the
session leaves the directory (`term` event `close`). Its transcript does not
die with it: when a session ends — or the daemon stops — the log and a little
metadata are written to `data/agent-history/` (per user × tile, the newest 20
per tile — on a partitioned tile per person's partition × tile, deleted by a
partition mode switch; a session that never took a prompt is not kept). `GET
/agent/history` lists them and `GET /agent/history/<id>/events` serves one in
this same shape, read-only; where the agent advertised `loadSession`, `POST
/term/sessions {resume:<id>}` reopens it (the agent replays the earlier turns
as events, then continues) and the continuation supersedes the entry.

## Tile ↔ shell messaging (window.postMessage)

Tiles are sandboxed opaque-origin iframes (ND8); a small `postMessage`
protocol between a tile's `xbin-client.js` and its embedding `<bx-frame>`
(relayed to `<bx-shell>`) backs the height, dialog, and pop-out-window
features. Every message is `{ type: "xbin:…", … }` and is only honored from
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
tile → parent  xbin:scroll-focus {}                  the pointer entered this document (cosmetic)
frame → tile   xbin:reply    {id, result}            dialog result / window closed
```

`xbin:open-deployments` (the admin console's runtime → deployments tab
links with it) asks the shell to open `tile`'s terminal window on its
Deployments layout, as the tile menu's "Deployments…" does. `<bx-frame>`
passes on only a well-formed tile path (no `.`/`..` segment), and
`<bx-shell>` opens it only for a tile its viewer's `/components` lists; the
panel then runs as the viewer, so the message grants nothing. Anything else
ignores it (the xbin app has no terminal window).

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
`xbin:window`, `xbin:window-close` and `xbin:contextmenu` posts (sender = the
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
  after ~30 min, and crash-loop-broken after 3 fast exits.
- While your deployment is pinned (docs/tile-deployments.md) you run a
  checkpoint instead of the work tree — read-only at the tile's own path,
  restarted from its kept build on every restart — and a save doesn't reach
  you: a crash loop clears with a deploy or a restart. Your env is the same.
- A partitioned tile (in development, docs/partitions.md) runs one process per person who
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
  edge policy (§Tile deployments), its cron jobs and bus subscriptions fire
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
