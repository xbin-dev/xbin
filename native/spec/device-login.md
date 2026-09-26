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

**From a browser.** The shell's 🔧 menu → my account → devices… → *add a
device* shows a QR code of

```
xbin://enroll?u=<origin>&c=<code>
```

(query-escaped; e.g. `xbin://enroll?u=https%3A%2F%2Fxbin.example.com&c=K3D…`).
`u` is the server origin (§4) — the address the app talks to **and** signs.
`c` is 26 characters of `A–Z2–7`, valid **5 minutes**, **single use**,
bound to the user who minted it. The app may accept it typed: case-
insensitive, dashes and spaces ignored.

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
user typed.

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
  (equal to the enrollment link's `u`): `scheme://host[:port]`, lower-case,
  no default port (`:443` for https, `:80` for http), no path, no trailing
  slash. It is the server's `--external-url` origin when the operator set one,
  otherwise the address the enrolling browser (or app) used. The server
  verifies against the origin it recorded **at enrollment**, not against the
  address a login request arrives on.
- `deviceId` — as returned at enrollment.
- `nonce` — the challenge string exactly as received (base64url, 43 chars).

## 5. In-app sign-in (before a device exists)

Both return the same token response as `POST /login/device` (without
`deviceId`). The app then enrolls (§2) with that session.

**Password.** The same rules as the web form — throttled, disabled accounts
refused, and under SSO-only mode non-admins get `403`:

```
POST /api/xbin/login
{"username": "…", "password": "…"}
→ 200 token response    401 invalid credentials    403 password sign-in disabled    429 throttled
```

**SSO** (only when the workspace has SSO configured; the login page's SSO
button is the tell). PKCE (RFC 7636, S256):

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
- Sign out: `POST /logout` with the bearer → `204` (the device stays
  enrolled; removing it is `DELETE /api/xbin/devices/<deviceId>`).
- The device list: `GET /api/xbin/devices` → `{"devices": [{"id", "name",
  "platform", "origin", "created", "lastUsed", "lastIP", "current"}]}`
  (`current`: the device this session signed in with).

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
