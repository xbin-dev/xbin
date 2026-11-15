# Protocol: core routes

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
the routes outside `/api/xbin/` — sign-in, component files
(`/c/`), the proxy to tile backends (`/api/<tile>/`), `/vendor/`, `/docs/`,
the built-in pages — and path tickets.

## Core

```
GET  /healthz                    200 "ok", unauthenticated (liveness)
GET  /login                      login page; ?token=<root> sets the admin cookie.
                                 ?next=<path>: where the sign-in lands — a
                                 same-origin path, not a sign-in route
                                 (else ignored); the form and the single
                                 sign-on link carry it on
POST /login                      {username,password[,next]} form → session
                                 cookie, 302 next (or /) (throttled)
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
                                 short-TTL cookie (throttled); ?next=<path>
                                 rides that cookie, and the callback lands
                                 there (the same rule as GET /login's)
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
                                 documents carry: protocol/deployments.md,
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
                                 sandboxed tile frames load it credential-less.
                                 Cache-Control: no-cache with a strong ETag
                                 (its content's SHA-256): a revalidation of
                                 an unchanged file is a 304; Vary: Origin
                                 (a sandboxed frame's Origin: null gets its
                                 CORS answer). A font asked for with its
                                 version, /vendor/fonts/<f>?v=<the first 8
                                 hex digits of its SHA-256> as theme.css
                                 writes it, is public, max-age=31536000,
                                 immutable; another ?v= is answered as
                                 without one (D184)
GET  /docs/<file>.md             these docs (HTML viewer for browsers; ?raw=1
                                 or non-HTML Accept for plain markdown)
GET  /xbin/partitions            the partitions page (docs/partitions.md
                                 §Your partitions page): xbind's own static
                                 page — web/partitions.html as shipped, no
                                 HTML transform, the same bytes for
                                 everyone — that reads and acts through
                                 /api/xbin/partitions… with the signed-in
                                 person's session. Any principal; signed
                                 out, a browser → /login?next=/xbin/
                                 partitions (it lands back here after
                                 signing in). Top-level only:
                                 X-Frame-Options DENY, CSP frame-ancestors
                                 'none' (with default-src and script-src
                                 'self', no inline script), COOP
                                 same-origin. /vendor/partitions.html is
                                 404: the page has no other copy
GET  /xbin/limits                 the tile limits page (D203,
                                 docs/isolation.md; ?tile=<t> opens one
                                 tile): xbind's own static page —
                                 web/limits.html as shipped, no
                                 transform — acting through /api/xbin/
                                 limits with the signed-in person's
                                 session; the "at all times" surface for
                                 a workspace whose scaffold predates
                                 limits. Headers, sign-in and /vendor/
                                 as /xbin/partitions
GET  /xbin/devices                the devices page (D204,
                                 docs/app-tunnel.md): xbind's own static
                                 page — web/devices.html as shipped, no
                                 transform — listing your app devices and
                                 pairing one over the app tunnel (its QR
                                 code and confirmation code), acting
                                 through /api/xbin/devices… with the
                                 signed-in person's session; the surface
                                 an older shell, or a workspace that
                                 requires the tunnel, pairs through.
                                 Headers, sign-in and /vendor/ as
                                 /xbin/partitions
GET  /xbin/system                 the system components page (D227,
                                 docs/sealed.md): xbind's own static page
                                 — web/system.html as shipped, no
                                 transform — showing an admin each system
                                 (sealed) component: its state and why it
                                 is held, its logs, who has access, its
                                 limits and what its slots are bound to;
                                 reading /api/xbin/sealed and acting
                                 through /access, /lifecycle and
                                 /bindings with the signed-in person's
                                 session. The surface for a workspace
                                 whose shell and admin console predate
                                 them. Headers, sign-in and /vendor/ as
                                 /xbin/partitions
GET  /xbin/llm                    the LLM connector's page (D226,
                                 docs/llm.md): xbind's own static page —
                                 web/llm.html as shipped, no transform —
                                 where a workspace admin sets the
                                 connector up (upstreams, default models,
                                 quotas, usage) through /api/xbin/llm…
                                 with their own session, and anyone else
                                 reads their own usage and quota; the
                                 surface a workspace whose admin console
                                 predates the connector uses, named by
                                 its alerts. Headers, sign-in and
                                 /vendor/ as /xbin/partitions
ANY  /api/<component-path>/<p>   → component backend (see below)
ANY  /api/xbin/<p>              → xbind's own API (protocol.md §xbind API)
ANY  /api/~<ticket>/<p>         → a path ticket's prefix of one tile's API
                                 (§Path tickets below, D135)
```

`/api/<component>` resolution is longest-prefix over registered components;
the remainder is the backend path, decoded: an encoded `/` (`%2F`) is a
separator there, and `.` and `..` segments are resolved (xbind's router
redirects a plain dot segment first). So a backend routes — and checks — the
path a request names: a caller can't smuggle a `/` into one segment's value
(the `/api/xbin` routes keep an encoded `/` in its segment instead).
A tile's bare `/api/<self>/` call reaches
the caller's own deployment (a self-call, at `admin`); anyone else's bare
call reaches the primary. `/api/<tile>+<name>/<p>` reaches deployment
`<name>` of a tile that has deployments: admins, and the tile's own
credentials bound to it; other tiles use the bare URL only, the primary's
name included; a person with write passes the URL gate and then holds no
role there, as on the bare URL. The qualifier is consumed: the backend sees
`/<p>` (protocol/deployments.md, *Deployment URLs*). Responses stream
(SSE/chunked flush immediately) and WebSocket upgrades pass through; a `?frame=` query
credential is accepted for browser WS attribution and is consumed by xbind
(stripped before forwarding). Backends with active streams are exempt from
idle reaping; streams still end at the callee's blue/green drain (D8).
**Path tickets** (D135). A tile's page mints one with `POST
/api/xbin/path-tickets {path}` (its frame token; protocol/users.md) for a prefix of
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
partition protocol.md §Authentication names (403 with the reason when it reaches none),
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
(protocol.md §Authentication, after the identity headers), and for an admin calling
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
(the partition refusals of protocol.md §Authentication). The client's
`xbin.fetch(url, {partition: 'global'})` and the Go SDK's
`xbin.GlobalURL(path)` add it, from a user partition only.
