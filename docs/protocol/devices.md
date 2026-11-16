# Protocol: the xbin app — devices, the app tunnel, push

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
what the xbin mobile app speaks — device enrolment, web
tickets, pairing, the app tunnel, push registrations, tile notifications
and Live Activities.

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Devices, enrolment and pairing

```
POST   /devices/enroll-code       a signed-in user (browser or app
                                   session), [{password, tunnel}].
                                   tunnel:true with the app tunnel on
                                   (D204) → {code, origin, expires, url:
                                   "xbin://enroll?u&c&tv=1&t&k&p[&r]
                                   [&rq=1]" (native/spec/tunnel.md §2),
                                   tunnel: {pairing, required, ticket,
                                   serverKey}}: a tunnel code, which
                                   never redeems over POST
                                   /devices/enroll — follow it with GET
                                   /devices/pairing/<pairing>; 503 when
                                   the tunnel isn't running. With the
                                   tunnel off, tunnel is ignored. 409
                                   {error, limit, count} at the person's
                                   device limit — for a tunnel code only
                                   at a limit of 0 (its confirmation
                                   names a device to replace); 409 while
                                   the workspace requires the tunnel and
                                   tunnel isn't asked (the error names
                                   <origin>/xbin/devices). Otherwise → {code, url:
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
                                   bad key, 409 at the person's device
                                   limit (GET /devices/limits; the error
                                   says how many they have and allows).
                                   A tunnel code answers 401 (it pairs
                                   over the app tunnel only, and isn't
                                   spent)
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
                                   lastIP, current, tunnel?}], limit,
                                   count} — own app devices; current marks
                                   the device behind the calling app
                                   session, tunnel one paired with the app
                                   tunnel (D204); limit: how many they may
                                   have, count: how many they have (past
                                   the limit only when it was lowered)
DELETE /devices/<id>              the device's user, or admin/xbin:users
                                   → {ok, user, dropped}: removes the key
                                   and ends every session it opened (and
                                   the frame and asset tokens they minted)
                                   and its push registration
GET    /users/<id>/devices        admin/xbin:users. {devices: […]} as
                                   GET /devices (no current), limit (their
                                   effective one), override? (maxDevices)
GET    /devices/limits            admin/xbin:users. {default, max: 32,
                                   atLimit: [row], overrides: [row]} — row
                                   {user, name?, override?, limit, count}:
                                   how many app devices one person may
                                   have paired (D204). default: the
                                   workspace's (1 unless set); atLimit:
                                   people at or past theirs; overrides:
                                   people with their own (PATCH
                                   /users/<id> {maxDevices})
PUT    /devices/limits            admin/xbin:users. {default: 1–32} → the
                                   GET view. Enforced when a device is
                                   added: devices already paired stay
GET    /devices/pairing/<id>      the login that minted the tunnel code
                                   (the same person and the same
                                   session; 404 for anyone else). {state:
                                   minted | handshaken | confirm |
                                   enrolled | rejected | abandoned |
                                   expired, sas? (six digits, while in
                                   confirm: show it grouped, "012 345"),
                                   device? {name, platform}, expires
                                   (unix: the code's, then the 2-minute
                                   confirmation window's), reason?
                                   (mismatch | expired | limit |
                                   refused), error?, deviceId?,
                                   replaced?, and while in confirm
                                   atLimit, limit, count (whether "Codes
                                   match" needs a device to replace)}. A
                                   finished pairing answers for 10
                                   minutes, then 404
POST   /devices/pairing/<id>/confirm
                                   the same login. {match: bool,
                                   replace?: <device id>} — "Codes
                                   match" enrolls the device (replace: one
                                   of yours it replaces, in the same
                                   step), "They don't" burns the code; →
                                   {state, deviceId?, replaced?,
                                   reason?}. 409 {error, limit, count} at
                                   the limit without replace (the pairing
                                   keeps waiting); 409 {error, state}
                                   when it no longer waits (abandoned:
                                   the phone left first — no device is
                                   made)
POST   /devices/<id>/tunnel       the device's own session, signed in
                                   within 10 minutes, over a direct
                                   connection. {noiseKey (base64url, 32
                                   bytes), endpoint (64 hex)} → as GET
                                   /app-tunnel/client: an enrolled device
                                   joins the tunnel without pairing again
                                   (native/spec/tunnel.md §9). 403 over
                                   the tunnel, while the workspace
                                   requires it, or for any other
                                   session; 409 tunnel off or the key
                                   another device's
```

## The app tunnel

```
GET    /app-tunnel                       admin. The app tunnel (D204,
                                         docs/app-tunnel.md): {enabled,
                                         tunnelOnly, relay: {mode: n0|custom,
                                         url?, tokenHint?, next?: {url?,
                                         tokenHint?}}, env? (XBIN_IROH_RELAY when
                                         it sets the relay), state: disabled |
                                         unavailable | starting | running |
                                         degraded | failed, confined, exec?,
                                         restarts, error?, endpointId?,
                                         homeRelay?, relayConnected, version?,
                                         devices: {total, tunnelPaired, classic,
                                         connected, synced}, conns: [{endpoint,
                                         conn, path, rttMs}]} — tokens only as
                                         hints (…ab12)
PUT    /app-tunnel                       admin. {enabled?, relay?: {mode:
                                         n0|custom, url?}, tunnelOnly?} → the
                                         view; turning tunnelOnly on adds
                                         lockedOut (the devices not paired with
                                         the tunnel it locks out). A custom
                                         relay is https:// (http only on
                                         loopback); changing it or enabled
                                         restarts the sidecar; tunnelOnly needs
                                         enabled, and disabling clears it. 400
                                         invalid; 409 when XBIN_IROH_RELAY sets
                                         the relay. Publishes a bodiless
                                         app-tunnel event — as does every
                                         change of the sidecar's state
                                         (starting → running, degraded,
                                         failed): re-read the view
POST   /app-tunnel/relay-tokens          admin, a custom relay. {action:
                                         generate-next | set-next | promote |
                                         drop-next | set-current | clear,
                                         token?, url?} → the view; generate-next
                                         adds token, the new token, shown this
                                         once. next is what devices learn ahead
                                         (GET /app-tunnel/client); promote makes
                                         it current and restarts the sidecar.
                                         Tokens: 16–256 printable characters, no
                                         whitespace. 409 for n0's relays
POST   /app-tunnel/test                  admin. {} → {reachable, path?, rttMs?,
                                         error?}: a throwaway endpoint dials
                                         this one through the relay
GET    /app-tunnel/client                a device's own session, over its
                                         tunnel. {endpointId, ticket, serverKey,
                                         relay: {url?, token?, next?: {url?,
                                         token?}}, required} — the device stores
                                         it after every connect and tries next
                                         when the current relay refuses it
                                         (native/spec/tunnel.md §7). 403
                                         anywhere else
```

## Push notifications

```
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
                                         device's Live Activities.
                                         agentStartHandle: the same for the
                                         workspace agent's card, whose
                                         ActivityKit type has its own token
                                         (D241; below)
GET    /devices/push                     a signed-in person. {workspace, enabled,
                                         devices:[{deviceId, kinds, created,
                                         updated, lastSent?, needsNewHandle?,
                                         relayError?, pushToStart?,
                                         activities?: [session], agentStart?,
                                         agentActivity?}], features} — your
                                         own. features: what this xbind's push
                                         plane does beyond the first version
                                         ("agent-push/1", "agent-actions/1",
                                         "agent-activity/1": the workspace
                                         agent's notifications, their actions'
                                         rule, its card — absent on an older
                                         xbind, which has none of them).
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
                                         session | ref | agent: true, handle,
                                         since?} → {activity: {session | agent,
                                         created, ended?}}. agent: true is the
                                         workspace agent's card (D241, below);
                                         a ref it was started by answers agent
                                         too. A Live Activity the device
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
                                         yours by that id. <session> @agent is
                                         the workspace agent's card: none comes
                                         back by push for the rest of its
                                         stretch
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
```

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
person whose partition was deleted (`tile.partition-deleted`), and — with
the workspace setting partitionConsent on — to a
person whose data in a partitioned tile another one's call was refused for
want of their consent (`tile.partition-consent`, at most one a day per
edge, collapse `partition-consent:<from>→<to>`), all linking
`xbin/partitions`; and a person's workspace agent waiting for them
(`agent.approval`, `agent.changeset`, `agent.ask`, `agent.done` — one a
thing, its words xbind's, never the agent's, the request's own fields
sealed with them; and the background `agent.clear` that takes them back:
§The workspace agent's notifications below). `xbin/partitions`
is the partitions page, `GET /xbin/partitions`, where
managers decide and people consent (an app that doesn't know the link
opens the workspace; `tile.partition-reset` and `account.credential` link
it too), all spending the person's budget and ignoring tile
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

**The workspace agent's notifications** (D241;
[workspace-agent.md](/docs/workspace-agent.md) §When the app is not open;
protocol/delegation.md §Events and pushes).
One notification a thing that waits for the person, after a 3 s grace (one
decided at once raises nothing): `agent.approval` a request (collapse
`agent-approval:<id>`, link `xbin/agent#r=<id>`), `agent.changeset` a plan,
admin changes or another person's ask to an admin (`agent-changeset:<id>`,
`xbin/agent#cs=<id>`), and from the person's agent itself, through `POST
/notify` — xbind writes the words for a delegation holder, whose title and
body never reach a screen — `agent.ask` its question and `agent.done` a
finished request (`agent-conv:<conversation>`, `xbin/agent#c=<conversation>`;
none per turn: the agent tells xbind when nothing of the request is open).
The sealed payload carries `agent` beside the text: `{v: 1, badge, request?
| changeset? | conversation?, ask?}` — `badge` what waits for the person in
the workspace; `request` `{id, kind, tile, tileName, expires, offer}` and,
only when `offer` is `once`, the call itself (`method, target, contentType,
body, bodySize, bodySha256`): xbind's lock-screen rule (a tile call whose
whole call fits a notification, nothing hidden, no warning) — the app
checks the same fields again, and an approval sent from a notification
(`via: "notification"`) is held to the rule once more. Past 12 at once,
then one a minute, single notifications stop and one summary (collapse
`agent-approval`, link `xbin/agent#needs`) goes out when the budget allows.
When the thing no longer waits — decided anywhere, withdrawn, expired, or
withdrawn by a switch — xbind sends `agent.clear` (`agent: {v, clear:
[collapse id], badge}`) as a **background** push (relay type `background`,
priority 5, nothing shown); a relay that doesn't list `background` (`GET
/v1/workspace` → `features`) gets none, and the app sweeps on foreground and
background refresh. A registration's `kinds` hold back what the person
turned off (the app lists the kinds it wants when one is off).

**The workspace agent's card** (D241). One Live Activity per person and
workspace while their agent works or something waits for them: the
Activity of session `@agent` (`POST /devices/push/activities {agent: true}`)
and, for push-to-start, the device's `agentStartHandle`. Its state is
`{phase: running | waiting | paused | idle, since, pending, agents}` —
counts only (`agents`: tile agents it started that are working). xbind
reads the person's picture on every delegate event, every status change of
their agent sessions and every model call of their agent (a turn runs for
90 s after one), coalesced (500 ms): a stretch begins when it works or
something waits (a push-to-start after 30 s on a device that shows none,
takes `agent.wsactivity` and didn't dismiss this stretch's — only on a relay
that lists `activity-agent`); idle, the card says so and ends 2 minutes
later; paused, it ends at once on `paused`; workspace agents off, the agent
taken away or held, it ends. To a relay without `activity-agent` xbind sends
the tile agents' fields only (no count; paused ends as idle; no start).

**Relay proof of work.** A relay may ask an anonymous workspace
registration for a proof of work (`xbin-relay -registration-pow <bits>`,
relay/README.md): xbind fetches its challenge (`GET
/v1/workspaces/challenge`), solves it — at most 28 bits; `PUT /push/config`
waits for it — and registers with it; a relay that asks for more answers
502 with a hint to get a key from its operator.
