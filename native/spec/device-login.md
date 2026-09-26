# Device login — the app-side contract

What the xbin app implements to sign in to a workspace (plans/native.md §5;
builder-facing summary: docs/auth.md §Device login; every route is also in
docs/protocol.md). The server side is `internal/server/devicelogin.go`,
`internal/auth/devices.go` and `internal/broker/devicesapi.go`. The test
vector at the end is `TestDeviceLoginVector` (internal/auth/devices_test.go)
— the Swift side must check itself against the same values.

Conventions: every body is JSON (`Content-Type: application/json`); binary
values are **base64url without padding** (RFC 4648 §5; the server tolerates
trailing `=` on input, never sends it); times are **unix seconds**. Errors are
`{"error": "…", "docs": "/docs/auth.md"}` with the status codes below. Every
unauthenticated route here counts failures against the login throttle
(5 failures per client IP → 30 s of `429`).

## 1. Keys

- One key per **workspace** (server URL + user): EC **P-256**, generated in
  the Secure Enclave (`SecureEnclave.P256.Signing.PrivateKey`, access control
  `.biometryCurrentSet`), non-exportable.
- `publicKey` = the key's **SubjectPublicKeyInfo DER** (CryptoKit:
  `publicKey.derRepresentation`), base64url.
- Signatures: **ECDSA with SHA-256**, encoded as **ASN.1 DER**
  (CryptoKit: `signature.derRepresentation`), base64url. A raw `r‖s`
  signature (`rawRepresentation`, WebCrypto's form) is refused.

## 2. Enrollment

A signed-in human mints a one-time code, the app redeems it with its public
key.

**From a browser.** The shell's top bar → **settings** → **add a device**
(the menu's first item; also *my account* → *devices…* → *add a device*)
shows a QR code of

```
xbin://enroll?u=<address>&c=<code>
```

(query-escaped; e.g. `xbin://enroll?u=https%3A%2F%2Fxbin.example.com&c=K3D…`).
`u` is the address the app **talks to**: by default the server origin
(§4), but the user may have typed another one in the panel's *address your
phone uses* — for a browser that reaches xbin through an SSH tunnel or a
proxy the phone can't use. It is always an `http(s)://host[:port]` origin
(no path), built in the browser; the server never sees it. **Sign the
`origin` of the enrollment response (Redeem, below; §4), never `u`**: the
two differ whenever the user chose an address, and a device signing `u`
would never sign in. Keep connecting to `u`.
`c` is 26 characters of `A–Z2–7`, valid **5 minutes**, **single use**,
bound to the user who minted it. The app may accept it typed: case-
insensitive, dashes and spaces ignored. Before redeeming, the app may ask
`u` for its sign-in methods (§8): an address that doesn't answer, or isn't
an xbin workspace, then fails before the code is spent.

**From the app itself.** After an in-app sign-in (§5) the app holds a session
and mints its own code — right away: minting is a **step-up**, allowed only
within **10 minutes** of the sign-in that opened the session (a device
outlives it), or with the account password in the body:

```
POST /api/xbin/devices/enroll-code          Authorization: Bearer <token>
[{"password": "…"}]                          optional; only needed past the 10 minutes
→ 200 {"code", "url", "origin", "expires"}
  403  {"error", "stepUp": "password"}  resend with the password (a wrong one → the same 403, throttled)
  403  {"error", "stepUp": "signin"}    sign in again (§5), then mint — an SSO account, or SSO-only mode
  403  not a user session (the bootstrap token and tiles have no account; no stepUp field)
  429  throttled
```

The browser path is held to the same rule (the shell asks for the password
when the sign-in is older).

**Adding another device from an enrolled one.** A device login (§3) is a
fresh signature with the enclave key, behind Face ID, so it counts as the
step-up: within **10 minutes** of `POST /login/device` the app mints a code
with its device session and no password — show it as a QR code for the
second device (it redeems it exactly like the browser's). The 10 minutes
count from the device login, not from the app's launch: past them, sign in
with the key again (a new challenge, one Face ID prompt) rather than
asking for the password, unless the user prefers to type it.

**Redeem** (no credential — the code is one):

```
POST /api/xbin/devices/enroll
{"code": "<c>", "name": "Łukasz's iPhone", "platform": "ios", "publicKey": "<spki>"}
→ 200 {"deviceId": "dev-…", "user": "<user id>", "origin": "<origin>", "name": "<as stored>"}
  400  bad JSON / missing fields / publicKey not an SPKI P-256 key (the code is NOT spent)
  401  code unknown, expired or already used (throttled)
  409  the user has 32 devices already, or the account was disabled meanwhile
```

`name` is trimmed, stripped of control characters and capped at 64
characters (`"device"` when empty); `platform` is lower-cased, ≤ 32 (`ios`,
`ipados`, `android`, …). Store `deviceId` **and** `origin` with the
workspace; the origin returned here is the one to sign (§4), whatever URL the
user typed or the link's `u` said — and keep talking to the address the
request went to.

## 3. Sign-in

```
POST /login/device/challenge
{"deviceId": "dev-…"}
→ 200 {"nonce": "<43 chars>", "expires": <unix>}      single use, 60 s, this device only
  404  unknown device — it was removed (or never enrolled here): enroll again
```

Sign `message` (§4) with the enclave key (this is the Face ID prompt), then:

```
POST /login/device
{"deviceId": "dev-…", "nonce": "<nonce as received>", "signature": "<der>"}
→ 200 {"token": "…", "tokenType": "Bearer",
       "user": {"id", "name", "role"},
       "deviceId": "dev-…",
       "expiresIdle": <unix>, "expiresMax": <unix>}
  401  nonce spent / expired / issued to another device, or the signature
       doesn't verify (throttled; the nonce is spent either way — get a new one)
  403  the account is disabled
  403  {"error", "reauth": "sso"}   an SSO-only account (below): this user's last SSO
       sign-in is older than the session max TTL (30 days by default). Run the
       SSO sign-in (§5) — the device stays enrolled — then device login works again
```

The nonce is consumed by the first `POST /login/device` naming it, success or
not. Ask for a fresh challenge per attempt; don't cache one.

**SSO-only accounts.** For an account whose only way in is SSO — a
non-admin in a workspace with password sign-in disabled, or, whenever SSO is
configured, an account with no password (provisioned by SSO, or an admin by
SSO group) — the IdP stays in charge: every SSO sign-in (web or app) opens a
window of the session max TTL in which device logins work, and a device
session's `expiresMax` never passes the end of that window (it can be much
sooner than 30 days after the device login). Plan the SSO re-auth around
`expiresMax` and the `reauth` answer.

## 4. The signed message

UTF-8 bytes of, with `\n` = one 0x0A byte and nothing after the nonce:

```
xbin-device-login-v1\n<origin>\n<deviceId>\n<nonce>
```

- `origin` — exactly the `origin` returned by `POST /api/xbin/devices/enroll`
  (usually equal to the enrollment link's `u` — **not** when the user chose
  an address for the phone, §2; sign this one): `scheme://host[:port]`, lower-case,
  no default port (`:443` for https, `:80` for http), no path, no trailing
  slash. It is the server's `--external-url` origin when the operator set one,
  otherwise the address the enrolling browser (or app) used. The server
  verifies against the origin it recorded **at enrollment**, not against the
  address a login request arrives on.
- `deviceId` — as returned at enrollment.
- `nonce` — the challenge string exactly as received (base64url, 43 chars).

## 5. In-app sign-in (before a device exists)

Both return the same token response as `POST /login/device` (without
`deviceId`). The app then enrolls (§2) with that session. Ask the workspace
which of the two it offers first (§8), and show those — an invite link is
§9.

**Password.** The same rules as the web form — throttled, disabled accounts
refused, and under SSO-only mode non-admins get `403`:

```
POST /api/xbin/login
{"username": "…", "password": "…"}
→ 200 token response    401 invalid credentials    403 password sign-in disabled    429 throttled
```

**SSO** (only when the workspace has SSO configured: §8's `sso.enabled`,
the same tell as the login page's SSO button; label the button with
`sso.label`). PKCE (RFC 7636, S256):

1. Pick `verifier` (43–128 chars of `A–Z a–z 0–9 - . _ ~`);
   `challenge = base64url(SHA-256(verifier))`.
2. Open `<origin>/login/sso?app=1&challenge=<challenge>` in
   `ASWebAuthenticationSession` with callback scheme `xbin`.
3. It ends at `xbin://sso?ticket=<ticket>` (one-shot, 2 minutes) — or
   `xbin://sso?error=<code>` (`denied`, `failed`, `noaccount`, `disabled`, …;
   the same codes the web login page explains).
4. Redeem:

```
POST /login/ticket
{"ticket": "<ticket>", "verifier": "<verifier>"}
→ 200 token response
  401  unknown / expired / used ticket, or the verifier doesn't match (the ticket is spent either way)
  403  the account is disabled
```

## 6. Using the session

- Send `Authorization: Bearer <token>` on every request the **app's own code**
  makes (tile list, whoami, `/api/xbin/frame-token`, `/ws/term`, agent
  sessions). It is the user's session: it reaches exactly what their browser
  session reaches. **Never** hand it to tile code — tiles get frame tokens.
- It dies after `expiresIdle` without use (every request slides it — so do
  frame-token renewals and tile requests made with frame tokens it minted),
  at `expiresMax` regardless, on an **xbind restart**, when the device is
  removed, on "sign out everywhere", or when the account is disabled. Any
  `401` ⇒ sign in again (§3); a `404` from the challenge ⇒ the device was
  removed: enroll again.
- Frame tokens minted with this session die with it on sign-out, device
  removal, "sign out everywhere" and disabling — reload those tiles after
  re-signing. An **xbind restart** is the exception: the session dies, but
  the frame tokens it minted keep working (and renewing) until the session
  would have expired, so open tiles needn't reload after a restart re-sign.
- Sign out: `POST /logout` with the bearer → `204`. It signs the device
  out, not just that token: every session opened with the device's key
  ends — earlier ones the app replaced without a logout (a 401 heal, a
  re-sign-in) and the Safari sessions any of them opened. The device stays
  enrolled; removing it is `DELETE /api/xbin/devices/<deviceId>`.
- The device list: `GET /api/xbin/devices` → `{"devices": [{"id", "name",
  "platform", "origin", "created", "lastUsed", "lastIP", "current"}]}`
  (`current`: the device this session signed in with).

**Opening the workspace in Safari, signed in.** Only a device-key session
(§3) may do this — not the §5 password/SSO session:

```
POST /api/xbin/web-ticket                    Authorization: Bearer <device session>
{"next": "/c/apps/calendar/"}                optional; default "/"
→ 200 {"url": "<origin>/login?ticket=<t>&next=<path>", "expires": <unix>, "expiresIn": 60}
  400  next is not a path on this workspace (one leading "/", printable ASCII —
       percent-encode the rest — no "\", ≤ 2048 characters, not /login… or /logout)
  403  not a device session, or the device was removed
  403  {"error", "reauth": "sso"}   an SSO-bound account past its window (§3)
  429  more than 10 per minute from this device (Retry-After)
```

Open `url` **at once, as is** in `SFSafariViewController` (not a web view
of the app, not by redirecting through another page): it is single use and
lives 60 seconds. What the browser then shows:

- **Signed out** (the usual case — Safari's cookie jar is not the app's): a
  page **"Continue as <name>"** naming the account, the login (and email)
  and the page it opens, with one button. Pressing it posts a one-shot
  nonce (2 minutes) back to `POST /login/web-ticket` from that page — the
  server takes it only as a same-origin form post (`Sec-Fetch-Site:
  same-origin`, or a matching `Origin`), only from the browser the page was
  served to (the nonce must equal a cookie that page's response set) — and
  only then opens the session and redirects to `next`. The extra tap is the
  login-CSRF defence: anyone can mint a link for *their* account and hand
  it to someone (a message, a QR code), and a link another app opens is a
  navigation "nobody started" (`Sec-Fetch-Site: none`) exactly like this
  app's, so no header can tell the two apart — the person has to see whose
  account it is. Don't try to skip or auto-submit the page.
- **Signed in as the same user:** straight to `next`, no new session.
- **Signed in as someone else, or opened from a page** (`Sec-Fetch-Site`
  other than `none`), an altered `next`, an expired/used link: a short text
  page (403) saying why.

The browser session it opens belongs to this device: signing the app out
of the workspace (`POST /logout` with the bearer) or removing the device
ends it, and it keeps the device login's time and cap (it is not a fresh
sign-in). `origin` is the device's enrollment origin (§4). When the app
talks to another address (the user chose one for the phone, §2), open the
url on **that** address instead — its path and query unchanged: the ticket
is not bound to a host name, and the enrollment origin may be one only the
user's browser can reach (a tunnel to `localhost`).

## 7. Test vector

A deterministic P-256 key (private scalar given so a client may re-derive
the public key; the signature is a fixed, valid, randomized ECDSA signature —
verify it, don't expect to reproduce it):

```
private scalar (hex)  56972329cd96fbb1c5ed326c9930baa3921e2c7660e0485e439293ce4432cd5f
publicKey (SPKI DER, base64url)
  MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEpVrcD7rXkoAfUgG-zMQbXbm6_8DAlKHGjvB5plisVHw_cypnpN6IdyZpAD3skiCZvuksA6pE7T7Iai4J8zi6Xw
publicKey (X9.63 uncompressed, hex)
  04a55adc0fbad792801f5201beccc41b5db9baffc0c094a1c68ef079a658ac547c3f732a67a4de88772669003dec922099bee92c03aa44ed3ec86a2e09f338ba5f
origin    https://xbin.example.com
deviceId  dev-00112233445566778899aabb
nonce     q2Yf0GJwq0t5J7m8sK0b3Qn9eW1xZ4vC6uR2pL8yT5A
message   "xbin-device-login-v1\nhttps://xbin.example.com\ndev-00112233445566778899aabb\nq2Yf0GJwq0t5J7m8sK0b3Qn9eW1xZ4vC6uR2pL8yT5A"
SHA-256(message) (hex)
  34a9d6a88b9e1a0e7f4bde58507c25b0ea9a5f2101ceb5c6e7abd2d414f55d2e
signature (ASN.1 DER, base64url)
  MEUCIBivy2f2ukCTylHbrik4FnmCw9rGMMivDcsqDMYb5YjHAiEA1o7xq4pOOITOwO4bUHKXsLICRr2_E2ey7sm8tvcGi54
```

Checks a client test should make: its message builder produces those bytes
(hash matches); `P256.Signing.PublicKey(derRepresentation:)` of the SPKI
equals the X9.63 key; `isValidSignature(ECDSASignature(derRepresentation:),
for: message)` is true; and false after changing any one of origin (including
a trailing `/`), deviceId or nonce.

## 8. Discovery: which sign-in methods

Public (no credential), not throttled, cheap — ask it before showing any
sign-in choice, and before spending a QR code:

```
GET /api/xbin/login/methods
→ 200 {"api": 1,
       "title": "<the workspace's branding title, else \"xbin\">",
       "auth": true,
       "password": {"enabled": true, "adminOnly": false},
       "sso": {"enabled": true, "label": "Sign in with Google"},
       "invites": true}
```

- `auth: false` — the workspace runs without sign-in (no-auth mode); then
  `password` and `sso` are disabled and `invites` is false. There is nothing
  for the app to sign in with.
- `password.enabled` — the workspace has accounts, so a password can sign
  someone in (`POST /api/xbin/login`, §5). `adminOnly: true` — SSO-only
  mode: non-admins sign in with SSO; lead with SSO and keep the password
  form for "workspace admin".
- `sso.enabled` — the login page shows its SSO button (configured, and the
  server has its external URL); `label` is that button's text ("" when
  disabled). Only then start §5's SSO flow.
- `invites` — invite links can be redeemed here (§9).
- `Cache-Control: no-store`. `api` versions the shape; fields are only
  ever added, so ignore unknown ones. It says nothing the login page
  doesn't already show anyone, and no server version.
- **An xbind older than this route does not answer 404**: its `/api/` gate
  runs before the route lookup, so it answers **`401`** with a text/plain
  body (`unauthorized — sign in at /login`) — or `404` when it runs
  without sign-in. So `401` or `404` here means "older xbind, or not an
  xbin workspace at all": to tell them apart, `GET /login` (an older xbind
  serves its HTML sign-in page, or redirects to `/` without sign-in) — then
  offer password and SSO as before; anything else is not a workspace. A
  `200` whose body isn't this JSON is not a workspace either.

## 9. Invites

An admin can create an account without a password and hand its owner a
single-use invite link, valid **72 hours**:
`<origin>/login?invite=<token>`. A browser opens it as a set-your-password
page; the app redeems the same token in JSON. Both routes are public (the
token is the credential) and under the login throttle — every refusal
counts as a failure (5 per client IP → 30 s of `429`).

```
POST /api/xbin/invite/check
{"invite": "<token>"}
→ 200 {"user": {"id": "erin", "name": "Erin Example"},
       "expires": <unix>, "title": "<branding title, else \"xbin\">"}
  403  {"error": "invalid or expired invite"}   unknown, used, expired, or the account is disabled
  429  throttled
```

It does **not** spend the invite: show "You're invited to <title> as
<name>", then ask for the new password (twice; at least 8 characters).

```
POST /api/xbin/invite/redeem
{"invite": "<token>", "password": "<new password>"}
→ 200 token response (as POST /api/xbin/login, §5)
  400  {"error": "password too short (min 8 characters)"}   the invite is NOT spent — ask again
  403  {"error": "invalid or expired invite"}   (as above; also a second redeem of the same token)
  429  throttled
```

Success sets the account's password, spends the invite and opens a session
exactly like the password sign-in — enroll the device with it right away
(§2, "From the app itself"). Talk to the origin of the invite link.
An xbind older than these routes answers them `401` (text/plain, its `/api/`
gate — see §8), a no-auth one `404`: tell the user to open the link in a
browser, set the password there, then sign in (§5). §8's `invites` says
beforehand whether a workspace redeems them.
